package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type OfferHandler struct {
	svc *services.OfferService
}

func NewOfferHandler(svc *services.OfferService) *OfferHandler {
	return &OfferHandler{svc: svc}
}

// Create godoc
// @Summary      Submit an offer against a service request
// @Description  garage_owner-only. Notifies the car owner in real time via WebSocket (offer_received).
// @Tags         offers
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path      string                        true  "Service Request ID"
// @Param        body  body      models.CreateOfferInput       true  "Offer details"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Router       /service-requests/{id}/offers [post]
func (h *OfferHandler) Create(c *fiber.Ctx) error {
	requestID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid service request id")
	}

	var in models.CreateOfferInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if in.GarageID == uuid.Nil || in.Price == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "garage_id and price are required")
	}
	in.ServiceRequestID = requestID
	if in.Currency == "" {
		in.Currency = "TZS"
	}

	id, status, err := h.svc.Create(c.Context(), in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to create offer")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id, "status": status})
}

// ListForRequest godoc
// @Summary      List offers on a service request
// @Tags         offers
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      string  true  "Service Request ID"
// @Success      200  {object}  map[string]interface{}
// @Router       /service-requests/{id}/offers [get]
func (h *OfferHandler) ListForRequest(c *fiber.Ctx) error {
	requestID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid service request id")
	}
	offers, err := h.svc.ListForRequest(c.Context(), requestID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to list offers")
	}
	return c.JSON(fiber.Map{"offers": offers})
}

// Accept godoc
// @Summary      Accept an offer (car_owner-only)
// @Description  Locks the request: rejects other offers, creates a booking, and notifies the winning garage via WebSocket (request_accepted).
// @Tags         offers
// @Security     BearerAuth
// @Produce      json
// @Param        offer_id  path      string  true  "Offer ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      403  {object}  apierr.Response
// @Router       /offers/{offer_id}/accept [post]
func (h *OfferHandler) Accept(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	offerID, err := uuid.Parse(c.Params("offer_id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid offer id")
	}

	result, err := h.svc.Accept(c.Context(), offerID, user.ID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusForbidden, err.Error())
	}

	return c.JSON(fiber.Map{
		"service_request_id": result.ServiceRequestID,
		"offer_id":           result.OfferID,
		"booking_id":         result.BookingID,
		"status":             "accepted",
	})
}
