package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
	swagger "github.com/gofiber/swagger"
	fiberws "github.com/gofiber/websocket/v2"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/auth"
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
	Health         *HealthHandler
	Garage         *GarageHandler
	ServiceRequest *ServiceRequestHandler
	Offer          *OfferHandler
	Booking        *BookingHandler
	Mechanic       *MechanicHandler
	Admin          *AdminHandler
	Commission     *CommissionHandler
	Review         *ReviewHandler
	ProfileRepo    repository.ProfileRepository
	WSManager      *ws.Manager
	Verifier       *auth.TokenVerifier
	// CORSAllowedOrigins is a comma-separated list of origins allowed to
	// call this API from a browser (web admin dashboard, and the
	// car-owner/provider apps' web builds — native/mobile builds aren't
	// subject to CORS at all, only browsers enforce it). Defaults to "*"
	// for easy local/trial testing; set a real comma-separated list of
	// your actual deployed origins in production (see CORS_ALLOWED_ORIGINS
	// in .env.example).
	CORSAllowedOrigins string
}

func NewRouter(d Deps, log zerolog.Logger) *fiber.App {
	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	app.Use(recover.New())
	app.Use(applog.FiberMiddleware(log))

	allowedOrigins := d.CORSAllowedOrigins
	if allowedOrigins == "" {
		allowedOrigins = "*"
	}
	app.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowMethods:     "GET,POST,PATCH,PUT,DELETE,OPTIONS",
		AllowHeaders:     "Origin, Content-Type, Accept, Authorization",
		AllowCredentials: allowedOrigins != "*", // browsers reject credentials+wildcard together
	}))

	app.Get("/healthz", d.Health.Get)
	// Silences the browser's automatic favicon request — this is a pure
	// API with no icon of its own; without this it falls through to a
	// protected route and 401s, which is harmless but noisy in the
	// console during web development.
	app.Get("/favicon.ico", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
	app.Get("/swagger/*", swagger.HandlerDefault)

	// --- WebSocket -------------------------------------------------
	app.Use("/ws", ws.UpgradeCheck(d.Verifier))
	app.Get("/ws", fiberws.New(ws.Handler(d.WSManager, log)))

	// --- Public / low-privilege ---------------------------------------
	garages := app.Group("/garages")
	garages.Get("/nearby", d.Garage.ListNearby)

	// --- Authenticated (any role) -------------------------------------
	auth := app.Group("", middleware.RequireAuth(d.Verifier), middleware.LoadProfile(d.ProfileRepo))

	// Open inbox for providers — auth only (no hard role gate). Role is still
	// checked softly in the handler so a mis-tagged profile can still see work
	// during demos; car owners get an empty list rather than a 403.
	auth.Get("/provider/open-requests", d.ServiceRequest.ListOpen)
	auth.Post("/transactions", d.Commission.RecordService)

	// car_owner routes
	carOwner := auth.Group("", middleware.RequireRole(models.RoleCarOwner))
	carOwner.Post("/service-requests", d.ServiceRequest.Create)
	carOwner.Get("/service-requests/mine", d.ServiceRequest.ListMine)
	carOwner.Post("/service-requests/:id/cancel", d.ServiceRequest.Cancel)
	carOwner.Post("/offers/:offer_id/accept", d.Offer.Accept)
	// The platform no longer takes payment. The car owner pays the
	// provider directly and attests to it here — this confirm call is
	// the single event that books the platform's 5% commission.
	carOwner.Post("/transactions/:id/confirm", d.Commission.Confirm)
	carOwner.Post("/transactions/:id/dispute", d.Commission.Dispute)
	auth.Post("/reviews", d.Review.Create)

	// shared read across roles (car owner viewing their own request,
	// garage/mechanic viewing a matched one — enforced further by RLS)
	auth.Get("/service-requests/:id", d.ServiceRequest.Get)
	auth.Get("/service-requests/:id/offers", d.Offer.ListForRequest)
	auth.Get("/service-requests/:id/booking", d.Booking.GetByRequest)

	// garage_owner routes
	// Provider-side commission routes. Mechanics as well as garage
	// owners record jobs and owe commission, so these hang off a group
	// that admits both rather than off garageOwner.
	provider := auth.Group("", middleware.RequireRole(models.RoleGarageOwner, models.RoleMechanic))
	provider.Get("/providers/me/balance", d.Commission.MyBalance)
	provider.Post("/settlements/ensure", d.Commission.EnsureSettlement)
	provider.Post("/settlements/:id/submit", d.Commission.SubmitSettlement)

	garageOwner := auth.Group("", middleware.RequireRole(models.RoleGarageOwner))
	garageOwner.Post("/garage-owner/verify", d.Garage.SubmitVerification)

	// garage_owner + mechanic — shared field work. Garage owners may also
	// act as on-road assistants (location updates + quotes + status).
	fieldRoles := auth.Group("", middleware.RequireRole(models.RoleGarageOwner, models.RoleMechanic))
	fieldRoles.Patch("/bookings/:id/status", d.Booking.SetStatus)
	fieldRoles.Patch("/mechanics/me/location", d.Mechanic.UpdateLocation)
	fieldRoles.Post("/service-requests/:id/offers", d.Offer.Create)
	// One-tap Approve / Deny for garage bookings and mechanic requests.
	fieldRoles.Post("/service-requests/:id/provider-respond", d.Offer.ProviderRespond)

	// admin-only — backs the web admin dashboard
	admin := auth.Group("/admin", middleware.RequireRole(models.RoleAdmin, models.RoleSuperAdmin))
	admin.Get("/debts", d.Commission.ListDebtors)
	admin.Post("/settlements/:id/verify", d.Commission.VerifySettlement)
	admin.Get("/garages/pending", d.Admin.ListPendingGarages)
	admin.Post("/garages/:id/approve", d.Admin.ApproveGarage)
	admin.Post("/garages/:id/reject", d.Admin.RejectGarage)
	admin.Delete("/garages/:id", d.Admin.DeleteGarage)
	admin.Delete("/mechanics/:id", d.Admin.DeleteMechanic)
	admin.Get("/service-requests", d.Admin.ListServiceRequests)
	admin.Post("/providers/:id/disable", d.Admin.DisableProvider)
	admin.Post("/providers/:id/enable", d.Admin.EnableProvider)

	return app
}
