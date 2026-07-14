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

// GetByRequest godoc
// @Summary      Resolve the booking for a service request
// @Description  The mobile apps only know a service_request_id until an offer is accepted; this resolves the resulting booking_id (needed for payment initiation and status updates).
// @Tags         bookings
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      string  true  "Service Request ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  apierr.Response
// @Router       /service-requests/{id}/booking [get]
func (h *BookingHandler) GetByRequest(c *fiber.Ctx) error {
	requestID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid service request id")
	}
	booking, err := h.svc.GetByRequest(c.Context(), requestID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusNotFound, "no booking yet for this request")
	}
	return c.JSON(booking)
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
