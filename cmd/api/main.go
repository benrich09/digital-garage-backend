
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/digital-garage/internal/auth"
	"github.com/yourorg/digital-garage/internal/config"
	"github.com/yourorg/digital-garage/internal/db"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/handlers"
	applog "github.com/yourorg/digital-garage/internal/logger"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/fcm"
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

	// Optional FCM push — if credentials missing, Notify is a no-op and
	// car owners still get real-time updates via WebSocket when online.
	var pushSvc *services.PushService
	tokenRepo := repository.NewDeviceTokenRepository(queries)
	fcmPath := os.Getenv("FCM_SERVICE_ACCOUNT_PATH")
	fcmProject := os.Getenv("FIREBASE_PROJECT_ID")
	if fcmPath != "" && fcmProject != "" {
		fcmClient, fcmErr := fcm.NewClientFromServiceAccountFile(ctx, fcmProject, fcmPath)
		if fcmErr != nil {
			log.Warn().Err(fcmErr).Msg("FCM client init failed — push notifications disabled")
		} else {
			pushSvc = services.NewPushService(tokenRepo, fcmClient, log)
			log.Info().Msg("FCM push notifications enabled")
		}
	} else {
		log.Info().Msg("FCM_SERVICE_ACCOUNT_PATH / FIREBASE_PROJECT_ID not set — WS-only notifications")
		// Still construct so RegisterToken works; Notify no-ops without fcm client
		pushSvc = services.NewPushService(tokenRepo, nil, log)
	}
	deviceHandler := handlers.NewDeviceHandler(pushSvc)

	// Wiring: repository -> service -> handler, all explicit, no DI
	// container. On a memory-constrained box, every reflection-based
	// wiring layer is bytes you don't get back; explicit constructor
	// calls cost nothing at runtime.
	profileRepo := repository.NewProfileRepository(queries)

	garageRepo := repository.NewGarageRepository(queries)
	garageSvc := services.NewGarageService(garageRepo)
	garageHandler := handlers.NewGarageHandler(garageSvc)

	requestRepo := repository.NewServiceRequestRepository(queries)
	requestSvc := services.NewServiceRequestService(requestRepo, garageRepo, hub, pool, log)
	requestHandler := handlers.NewServiceRequestHandler(requestSvc)

	offerRepo := repository.NewOfferRepository(pool, queries)
	offerSvc := services.NewOfferService(offerRepo, requestRepo, garageRepo, hub, log).WithPool(pool).WithPush(pushSvc)
	offerHandler := handlers.NewOfferHandler(offerSvc)

	bookingRepo := repository.NewBookingRepository(queries)
	bookingSvc := services.NewBookingService(bookingRepo, requestRepo, hub, log).WithPool(pool)
	bookingHandler := handlers.NewBookingHandler(bookingSvc)

	mechanicRepo := repository.NewMechanicRepository(queries)
	mechanicSvc := services.NewMechanicService(mechanicRepo, bookingRepo, requestRepo, hub, log).WithPool(pool)
	mechanicHandler := handlers.NewMechanicHandler(mechanicSvc)

	adminHandler := handlers.NewAdminHandler(garageSvc).WithPool(pool)

	// Payment gateways removed. The platform no longer moves money: car
	// owners pay providers directly (cash / their own mobile money) and
	// only confirm in the app. What we track is the 5% commission each
	// confirmed job creates as a debt from the provider, settled monthly
	// into our account. See migration 0013 and CommissionService.
	txnRepo := repository.NewServiceTransactionRepository(pool)
	ledgerRepo := repository.NewCommissionLedgerRepository(pool)
	settlementRepo := repository.NewSettlementRepository(pool)
	commissionSvc := services.NewCommissionService(txnRepo, ledgerRepo, settlementRepo, hub, log).WithPool(pool)
	commissionHandler := handlers.NewCommissionHandler(commissionSvc)
	bookingSvc.WithCommission(commissionSvc)

	reviewRepo := repository.NewReviewRepository(queries)
	reviewSvc := services.NewReviewService(reviewRepo, bookingRepo, requestRepo).WithPool(pool)
	reviewHandler := handlers.NewReviewHandler(reviewSvc)

	jobLife := services.NewJobLifecycleService(pool, hub, log).WithPush(pushSvc)
	jobHandler := handlers.NewJobHandler(jobLife)
	reportHandler := handlers.NewReportHandler(pool)

	healthHandler := handlers.NewHealthHandler(pool)

	app := handlers.NewRouter(handlers.Deps{
		Health:             healthHandler,
		Garage:             garageHandler,
		ServiceRequest:     requestHandler,
		Offer:              offerHandler,
		Booking:            bookingHandler,
		Mechanic:           mechanicHandler,
		Admin:              adminHandler,
		Commission:         commissionHandler,
		Review:             reviewHandler,
		Job:                jobHandler,
		Report:             reportHandler,
		Device:             deviceHandler,
		ProfileRepo:        profileRepo,
		WSManager:          hub,
		Verifier:           auth.NewTokenVerifier(cfg.SupabaseURL, cfg.SupabaseJWTSecret),
		CORSAllowedOrigins: cfg.CORSAllowedOrigins,
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
