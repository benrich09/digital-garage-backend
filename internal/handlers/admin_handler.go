package handlers

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

// AdminHandler backs the web admin dashboard's garage-review queue.
// Every route here is mounted behind RequireRole(models.RoleAdmin).
type AdminHandler struct {
	garageSvc *services.GarageService
	// Optional: service-request listing for the admin track board.
	// Wired when available; list endpoints tolerate nil.
	srSvc *services.ServiceRequestService
	pool  *pgxpool.Pool
}

func NewAdminHandler(garageSvc *services.GarageService) *AdminHandler {
	return &AdminHandler{garageSvc: garageSvc}
}

func (h *AdminHandler) WithServiceRequests(svc *services.ServiceRequestService) *AdminHandler {
	h.srSvc = svc
	return h
}

func (h *AdminHandler) WithPool(pool *pgxpool.Pool) *AdminHandler {
	h.pool = pool
	return h
}

// DisableProvider sets profiles.is_active = false so the provider can no
// longer log in or receive new work. Used when a provider accumulates
// unpaid commission debt beyond the admin threshold.
func (h *AdminHandler) DisableProvider(c *fiber.Ctx) error {
	if h.pool == nil {
		return apierr.JSON(c, fiber.StatusNotImplemented, "disable not wired")
	}
	providerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid provider id")
	}
	tag, err := h.pool.Exec(c.Context(), `
		update profiles
		set is_active = false, updated_at = now()
		where id = $1 and role in ('garage_owner', 'mechanic')
	`, providerID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to disable provider")
	}
	if tag.RowsAffected() == 0 {
		return apierr.JSON(c, fiber.StatusNotFound, "provider not found or not a provider role")
	}
	return c.JSON(fiber.Map{"provider_id": providerID, "status": "disabled", "is_active": false})
}

// EnableProvider re-activates a previously disabled provider.
func (h *AdminHandler) EnableProvider(c *fiber.Ctx) error {
	if h.pool == nil {
		return apierr.JSON(c, fiber.StatusNotImplemented, "enable not wired")
	}
	providerID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid provider id")
	}
	tag, err := h.pool.Exec(c.Context(), `
		update profiles
		set is_active = true, updated_at = now()
		where id = $1 and role in ('garage_owner', 'mechanic')
	`, providerID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to enable provider")
	}
	if tag.RowsAffected() == 0 {
		return apierr.JSON(c, fiber.StatusNotFound, "provider not found or not a provider role")
	}
	return c.JSON(fiber.Map{"provider_id": providerID, "status": "enabled", "is_active": true})
}


// ListPendingGarages godoc
// @Summary      List garages awaiting verification
// @Tags         admin
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Router       /admin/garages/pending [get]
func (h *AdminHandler) ListPendingGarages(c *fiber.Ctx) error {
	garages, err := h.garageSvc.ListPending(c.Context())
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to list pending garages")
	}
	return c.JSON(fiber.Map{"garages": garages})
}

// ApproveGarage godoc
// @Summary      Approve a pending garage
// @Tags         admin
// @Security     BearerAuth
// @Param        id   path      string  true  "Garage ID"
// @Success      200  {object}  map[string]interface{}
// @Router       /admin/garages/{id}/approve [post]
func (h *AdminHandler) ApproveGarage(c *fiber.Ctx) error {
	admin, _ := middleware.CurrentUser(c)
	garageID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid garage id")
	}
	if err := h.garageSvc.Approve(c.Context(), garageID, admin.ID); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to approve garage")
	}
	return c.JSON(fiber.Map{"garage_id": garageID, "status": "approved"})
}

// RejectGarage godoc
// @Summary      Reject a pending garage
// @Tags         admin
// @Security     BearerAuth
// @Param        id   path      string  true  "Garage ID"
// @Success      200  {object}  map[string]interface{}
// @Router       /admin/garages/{id}/reject [post]
func (h *AdminHandler) RejectGarage(c *fiber.Ctx) error {
	admin, _ := middleware.CurrentUser(c)
	garageID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid garage id")
	}
	if err := h.garageSvc.Reject(c.Context(), garageID, admin.ID); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to reject garage")
	}
	return c.JSON(fiber.Map{"garage_id": garageID, "status": "rejected"})
}


// DeleteGarage soft-deletes / deactivates a garage (admin).
func (h *AdminHandler) DeleteGarage(c *fiber.Ctx) error {
	garageID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid garage id")
	}
	if err := h.garageSvc.Deactivate(c.Context(), garageID); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to delete garage")
	}
	return c.JSON(fiber.Map{"garage_id": garageID, "status": "deleted"})
}

// DeleteMechanic removes a mechanic profile row (admin).
func (h *AdminHandler) DeleteMechanic(c *fiber.Ctx) error {
	mechanicID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid mechanic id")
	}
	if err := h.garageSvc.DeleteMechanic(c.Context(), mechanicID); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to delete mechanic")
	}
	return c.JSON(fiber.Map{"mechanic_id": mechanicID, "status": "deleted"})
}

// ListServiceRequests returns open requests near optional lat/lng for the admin board.
// Query params: lat, lng, radius_km (all optional). Without coordinates, returns a hint
// — the admin web app typically reads service_requests via Supabase directly.
func (h *AdminHandler) ListServiceRequests(c *fiber.Ctx) error {
	if h.srSvc == nil {
		return apierr.JSON(c, fiber.StatusNotImplemented, "service request listing not wired")
	}

	latStr := c.Query("lat")
	lngStr := c.Query("lng")
	if latStr == "" || lngStr == "" {
		return c.JSON(fiber.Map{
			"hint":             "Pass ?lat=&lng= to list open requests near a point, or read service_requests via Supabase in the admin UI.",
			"service_requests": []any{},
		})
	}

	lat, err1 := strconv.ParseFloat(latStr, 64)
	lng, err2 := strconv.ParseFloat(lngStr, 64)
	if err1 != nil || err2 != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "lat and lng must be numbers")
	}
	radiusKM, _ := strconv.ParseFloat(c.Query("radius_km"), 64)

	requests, err := h.srSvc.BrowseOpen(c.Context(), lat, lng, radiusKM, 50)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to list service requests")
	}
	return c.JSON(fiber.Map{"service_requests": requests})
}
