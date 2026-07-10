package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

// AdminHandler backs the web admin dashboard's garage-review queue.
// Every route here is mounted behind RequireRole(models.RoleAdmin).
type AdminHandler struct {
	garageSvc *services.GarageService
}

func NewAdminHandler(garageSvc *services.GarageService) *AdminHandler {
	return &AdminHandler{garageSvc: garageSvc}
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
