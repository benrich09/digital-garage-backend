package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type ServiceRequestHandler struct {
	svc *services.ServiceRequestService
}

func NewServiceRequestHandler(svc *services.ServiceRequestService) *ServiceRequestHandler {
	return &ServiceRequestHandler{svc: svc}
}

// Create godoc
// @Summary      Create a service request
// @Description  car_owner-only. photo_urls are Supabase Storage URLs the app already uploaded to directly — this API never receives image bytes. Triggers a new_request_match WebSocket event to nearby garages offering that category.
// @Tags         service-requests
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      models.CreateServiceRequestInput  true  "Request details"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      401  {object}  apierr.Response
// @Router       /service-requests [post]
func (h *ServiceRequestHandler) Create(c *fiber.Ctx) error {
	userIDStr, ok := middleware.UserID(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	ownerID, err := uuid.Parse(userIDStr)
	if err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, "invalid user id in token")
	}

	var in models.CreateServiceRequestInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if in.VehicleID == uuid.Nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "vehicle_id is required")
	}
	if in.CategoryID == uuid.Nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "category_id is required")
	}
	if in.Latitude == 0 && in.Longitude == 0 {
		return apierr.JSON(c, fiber.StatusBadRequest, "latitude and longitude are required")
	}

	id, status, err := h.svc.Create(c.Context(), ownerID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to create service request")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id, "status": status})
}

// ListMine godoc
// @Summary      List the caller's own service requests
// @Tags         service-requests
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Router       /service-requests/mine [get]
func (h *ServiceRequestHandler) ListMine(c *fiber.Ctx) error {
	userIDStr, ok := middleware.UserID(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	ownerID, err := uuid.Parse(userIDStr)
	if err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, "invalid user id in token")
	}

	requests, err := h.svc.ListMine(c.Context(), ownerID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to list service requests")
	}

	return c.JSON(fiber.Map{"service_requests": requests})
}

// Get godoc
// @Summary      Get a service request by ID
// @Tags         service-requests
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      string  true  "Service Request ID"
// @Success      200  {object}  models.ServiceRequest
// @Failure      404  {object}  apierr.Response
// @Router       /service-requests/{id} [get]
func (h *ServiceRequestHandler) Get(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid id")
	}

	req, err := h.svc.Get(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusNotFound, "service request not found")
	}

	return c.JSON(req)
}
