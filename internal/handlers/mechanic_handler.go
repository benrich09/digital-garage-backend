package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type MechanicHandler struct {
	svc *services.MechanicService
}

func NewMechanicHandler(svc *services.MechanicService) *MechanicHandler {
	return &MechanicHandler{svc: svc}
}

type updateLocationInput struct {
	BookingID string  `json:"booking_id"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// UpdateLocation godoc
// @Summary      Push the mechanic's current GPS position during an active job
// @Description  mechanic-only. Called periodically by the Flutter app. Pushes a status_update WebSocket event with mechanic_lat/mechanic_lng (plain floats) so the car owner's Google Map marker updates live.
// @Tags         mechanics
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      updateLocationInput  true  "Current position"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      403  {object}  apierr.Response
// @Router       /mechanics/me/location [patch]
func (h *MechanicHandler) UpdateLocation(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}

	var in updateLocationInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	bookingID, err := uuid.Parse(in.BookingID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking_id")
	}

	if err := h.svc.UpdateLocation(c.Context(), user.ID, bookingID, in.Latitude, in.Longitude); err != nil {
		return apierr.JSON(c, fiber.StatusForbidden, err.Error())
	}

	return c.JSON(fiber.Map{"status": "ok"})
}
