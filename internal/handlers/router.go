package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	swagger "github.com/gofiber/swagger"
	fiberws "github.com/gofiber/websocket/v2"
	"github.com/rs/zerolog"
	applog "github.com/yourorg/digital-garage/internal/logger"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"

	// After running `swag init -g cmd/api/main.go -o docs` (see README),
	// uncomment this import so swagger.HandlerDefault below has docs to
	// serve. Left out here so the project compiles before you've
	// generated anything.
	// _ "github.com/yourorg/digital-garage/docs"
)

// Deps bundles every handler + shared dependency the router needs.
// Keeping this as a plain struct (rather than a DI framework) is one
// more small choice in favor of low overhead: no reflection-based
// container, no runtime graph resolution — just explicit constructor
// calls in main.go.
//
// This single router serves ALL THREE clients — the car-owner app, the
// garage/mechanic app, and the web admin dashboard — over the same set
// of REST + WebSocket routes. Nothing here is dashboard-only or
// mobile-only; RBAC (via RequireRole) is what actually differentiates
// what each authenticated caller can do, not a separate API surface.
type Deps struct {
	Health          *HealthHandler
	Garage          *GarageHandler
	ServiceRequest  *ServiceRequestHandler
	Offer           *OfferHandler
	Booking         *BookingHandler
	Mechanic        *MechanicHandler
	Admin           *AdminHandler
	Payment         *PaymentHandler
	Review          *ReviewHandler
	ProfileRepo     repository.ProfileRepository
	WSManager       *ws.Manager
	JWTSecret       string
}

func NewRouter(d Deps, log zerolog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	app.Use(applog.FiberMiddleware(log))

	app.Get("/healthz", d.Health.Get)
	app.Get("/swagger/*", swagger.HandlerDefault)

	// --- WebSocket -------------------------------------------------
	app.Use("/ws", ws.UpgradeCheck(d.JWTSecret))
	app.Get("/ws", fiberws.New(ws.Handler(d.WSManager, log)))

	// --- Public / low-privilege ---------------------------------------
	garages := app.Group("/garages")
	garages.Get("/nearby", d.Garage.ListNearby)

	// Flutterwave calls this directly — no user JWT to check, the
	// handler verifies the verif-hash header against our own secret
	// instead. Must stay outside the `auth` group below.
	app.Post("/webhooks/flutterwave", d.Payment.Webhook)

	// --- Authenticated (any role) -------------------------------------
	auth := app.Group("", middleware.RequireAuth(d.JWTSecret), middleware.LoadProfile(d.ProfileRepo))

	// car_owner routes
	carOwner := auth.Group("", middleware.RequireRole(models.RoleCarOwner))
	carOwner.Post("/service-requests", d.ServiceRequest.Create)
	carOwner.Get("/service-requests/mine", d.ServiceRequest.ListMine)
	carOwner.Post("/offers/:offer_id/accept", d.Offer.Accept)
	carOwner.Post("/payments/initiate", d.Payment.Initiate)
	carOwner.Post("/reviews", d.Review.Create)

	// shared read across roles (car owner viewing their own request,
	// garage/mechanic viewing a matched one — enforced further by RLS)
	auth.Get("/service-requests/:id", d.ServiceRequest.Get)
	auth.Get("/service-requests/:id/offers", d.Offer.ListForRequest)

	// garage_owner routes
	garageOwner := auth.Group("", middleware.RequireRole(models.RoleGarageOwner))
	garageOwner.Post("/garage-owner/verify", d.Garage.SubmitVerification)
	garageOwner.Post("/service-requests/:id/offers", d.Offer.Create)

	// garage_owner + mechanic routes
	fieldRoles := auth.Group("", middleware.RequireRole(models.RoleGarageOwner, models.RoleMechanic))
	fieldRoles.Patch("/bookings/:id/status", d.Booking.SetStatus)

	// mechanic-only
	mechanic := auth.Group("", middleware.RequireRole(models.RoleMechanic))
	mechanic.Patch("/mechanics/me/location", d.Mechanic.UpdateLocation)

	// admin-only — backs the web admin dashboard
	admin := auth.Group("/admin", middleware.RequireRole(models.RoleAdmin))
	admin.Get("/garages/pending", d.Admin.ListPendingGarages)
	admin.Post("/garages/:id/approve", d.Admin.ApproveGarage)
	admin.Post("/garages/:id/reject", d.Admin.RejectGarage)

	return app
}
