// Command api is the single binary for the Digital Garage backend. It's
// intentionally one process, one binary, no background worker pool, no
// separate migration runner baked in — all of that keeps the footprint
// small enough for a 1 vCPU / 1GB VM. Schema migrations are applied
// separately via `supabase db push` (see supabase/migrations), not from
// this binary, so the API process never needs elevated DB privileges.
//
// This single binary serves the car-owner app, the garage/mechanic app,
// AND the web admin dashboard — they all hit the same REST + WebSocket
// routes, differentiated only by the authenticated caller's role (see
// internal/middleware/rbac.go). There's no separate "admin API".
//
// @title           Digital Garage API
// @version         1.0
// @description     Backend for the Digital Garage marketplace: car owners, garages/mechanics, and the web admin dashboard all talk to this one API.
// @BasePath        /
// @securityDefinitions.apikey  BearerAuth
// @in                          header
// @name                        Authorization
// @description                 Type "Bearer" followed by a space and the Supabase access token.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/digital-garage/internal/config"
	"github.com/yourorg/digital-garage/internal/db"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/handlers"
	applog "github.com/yourorg/digital-garage/internal/logger"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/fcm"
	"github.com/yourorg/digital-garage/pkg/mpesa"
	"github.com/yourorg/digital-garage/pkg/selcom"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err) // logger isn't up yet; nothing to do but exit loudly
	}

	log := applog.New(cfg.Env, cfg.LogLevel)
	log.Info().Str("env", cfg.Env).Msg("starting digital-garage api")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns, cfg.DBMinConns, log)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	queries := sqlcgen.New(pool)

	// The WebSocket hub is a singleton shared by every service that
	// needs to push real-time events — this is the whole "pub/sub"
	// layer at single-instance scale (see internal/ws/events.go for the
	// note on introducing Redis Pub/Sub if this ever becomes multi-instance).
	hub := ws.NewManager(log)

	// Wiring: repository -> service -> handler, all explicit, no DI
	// container. On a memory-constrained box, every reflection-based
	// wiring layer is bytes you don't get back; explicit constructor
	// calls cost nothing at runtime.
	profileRepo := repository.NewProfileRepository(queries)

	// FCM is optional at startup — a missing project ID just means
	// PushService.Notify() no-ops (see its comment), so local dev
	// without Firebase set up still runs fine.
	var fcmClient *fcm.Client
	if cfg.FirebaseProjectID != "" && cfg.FirebaseServiceAccountFile != "" {
		client, err := fcm.NewClientFromServiceAccountFile(ctx, cfg.FirebaseProjectID, cfg.FirebaseServiceAccountFile)
		if err != nil {
			log.Warn().Err(err).Msg("FCM not configured correctly — push notifications disabled")
		} else {
			fcmClient = client
		}
	}
	deviceTokenRepo := repository.NewDeviceTokenRepository(queries)
	pushService := services.NewPushService(deviceTokenRepo, fcmClient, log)
	deviceHandler := handlers.NewDeviceHandler(pushService)

	garageRepo := repository.NewGarageRepository(queries)
	garageSvc := services.NewGarageService(garageRepo)
	garageHandler := handlers.NewGarageHandler(garageSvc)

	requestRepo := repository.NewServiceRequestRepository(queries)
	requestSvc := services.NewServiceRequestService(requestRepo, garageRepo, hub, pushService, log)
	requestHandler := handlers.NewServiceRequestHandler(requestSvc)

	offerRepo := repository.NewOfferRepository(pool, queries)
	offerSvc := services.NewOfferService(offerRepo, requestRepo, garageRepo, hub, pushService, log)
	offerHandler := handlers.NewOfferHandler(offerSvc)

	bookingRepo := repository.NewBookingRepository(queries)
	bookingSvc := services.NewBookingService(bookingRepo, requestRepo, hub, pushService, log)
	bookingHandler := handlers.NewBookingHandler(bookingSvc)

	mechanicRepo := repository.NewMechanicRepository(queries)
	mechanicSvc := services.NewMechanicService(mechanicRepo, bookingRepo, requestRepo, hub, log)
	mechanicHandler := handlers.NewMechanicHandler(mechanicSvc)

	adminHandler := handlers.NewAdminHandler(garageSvc)

	var mpesaClient *mpesa.Client
	if cfg.MpesaConsumerKey != "" && cfg.MpesaShortcode != "" {
		mpesaClient = mpesa.NewClient(cfg.MpesaConsumerKey, cfg.MpesaConsumerSecret, cfg.MpesaShortcode, cfg.MpesaPasskey, cfg.MpesaBaseURL, cfg.MpesaCallbackURL)
	} else {
		log.Warn().Msg("M-Pesa not configured — /payments/initiate with provider=mpesa will fail until MPESA_* env vars are set")
	}

	var selcomClient *selcom.Client
	if cfg.SelcomVendorID != "" && cfg.SelcomAPIKey != "" {
		selcomClient = selcom.NewClient(cfg.SelcomVendorID, cfg.SelcomAPIKey, cfg.SelcomAPISecret, cfg.SelcomBaseURL)
	} else {
		log.Warn().Msg("Selcom not configured — /payments/initiate with provider=selcom will fail until SELCOM_* env vars are set")
	}

	paymentRepo := repository.NewPaymentRepository(queries)
	paymentSvc := services.NewPaymentService(paymentRepo, bookingRepo, offerRepo, requestRepo, mpesaClient, selcomClient, hub, log)
	paymentHandler := handlers.NewPaymentHandler(paymentSvc, cfg.MpesaCallbackSecret)

	reviewRepo := repository.NewReviewRepository(queries)
	reviewSvc := services.NewReviewService(reviewRepo, bookingRepo, requestRepo)
	reviewHandler := handlers.NewReviewHandler(reviewSvc)

	healthHandler := handlers.NewHealthHandler(pool)

	app := handlers.NewRouter(handlers.Deps{
		Health:         healthHandler,
		Garage:         garageHandler,
		ServiceRequest: requestHandler,
		Offer:          offerHandler,
		Booking:        bookingHandler,
		Mechanic:       mechanicHandler,
		Admin:          adminHandler,
		Payment:        paymentHandler,
		Review:         reviewHandler,
		Device:         deviceHandler,
		ProfileRepo:    profileRepo,
		WSManager:      hub,
		JWTSecret:      cfg.SupabaseJWTSecret,
	}, log)

	go func() {
		log.Info().Str("port", cfg.Port).Msg("http server listening")
		if err := app.Listen(":" + cfg.Port); err != nil {
			log.Fatal().Err(err).Msg("http server failed")
		}
	}()

	<-ctx.Done()
	log.Info().Msg("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown failed")
	}
	log.Info().Msg("server stopped")
}
