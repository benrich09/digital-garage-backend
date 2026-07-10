package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type BookingHandler struct {
	svc *services.BookingService
}

func NewBookingHandler(svc *services.BookingService) *BookingHandler {
	return &BookingHandler{svc: svc}
}

type setBookingStatusInput struct {
	Status string `json:"status"` // "in_progress" | "completed" | "cancelled"
}

// SetStatus godoc
// @Summary      Update a booking's status
// @Description  garage_owner/mechanic-only. Notifies the car owner via WebSocket (status_update, or job_completed when status=completed).
// @Tags         bookings
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path      string                  true  "Booking ID"
// @Param        body  body      setBookingStatusInput   true  "New status"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Router       /bookings/{id}/status [patch]
func (h *BookingHandler) SetStatus(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	var in setBookingStatusInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if err := h.svc.SetStatus(c.Context(), id, in.Status); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(fiber.Map{"booking_id": id, "status": in.Status})
}
