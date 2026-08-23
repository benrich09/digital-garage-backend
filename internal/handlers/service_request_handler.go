package handlers

import (
	"strconv"
	"strings"

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
	// vehicle_id is preferred but not required (nullable FK)
	if in.Latitude == 0 && in.Longitude == 0 {
		// Default Dar centre so demos still create a matchable request.
		in.Latitude = -6.7924
		in.Longitude = 39.2083
	}

	id, status, err := h.svc.Create(c.Context(), ownerID, in)
	if err != nil {
		// Surface the real cause so the app can show it (FK, role, missing category, etc.)
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
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

// ListOpen godoc
// @Summary      Browse open (pending) requests near a point
// @Description  Providers call this on load to see requests created while
// @Description  they were offline. lat/lng are the provider's garage or
// @Description  current location; radius_km is optional (defaults server-side).
// @Tags         service-requests
// @Security     BearerAuth
// @Produce      json
// @Param        lat        query  number  true   "Latitude"
// @Param        lng        query  number  true   "Longitude"
// @Param        radius_km  query  number  false  "Search radius in km"
// @Success      200  {object}  map[string][]models.OpenServiceRequest
// @Router       /provider/open-requests [get]
func (h *ServiceRequestHandler) ListOpen(c *fiber.Ctx) error {
	role := ""
	userID := ""
	if user, ok := middleware.CurrentUser(c); ok {
		role = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(user.Role), " ", "_"))
		userID = user.ID.String()
		switch role {
		case "garage_owner", "garage-owner", "garageowner", "garage":
			role = "garage_owner"
		case "mechanic", "mechanics":
			role = "mechanic"
		case "car_owner", "customer", "owner":
			// Not a provider — empty inbox
			return c.JSON(fiber.Map{"service_requests": []any{}, "count": 0})
		}
	}

	lat, err1 := strconv.ParseFloat(c.Query("lat"), 64)
	lng, err2 := strconv.ParseFloat(c.Query("lng"), 64)
	if err1 != nil || err2 != nil {
		lat, lng = -6.7924, 39.2083
	}
	radiusKM, _ := strconv.ParseFloat(c.Query("radius_km"), 64)
	if radiusKM <= 0 {
		radiusKM = 500
	}

	requests, err := h.svc.BrowseOpen(c.Context(), lat, lng, radiusKM, 50)
	if err != nil {
		requests = nil
	}

	// Ultimate fallback: raw pending rows from pool (no PostGIS, no joins required)
	if len(requests) == 0 && h.svc != nil {
		if extra, e2 := h.svc.ListPendingSimple(c.Context(), 50); e2 == nil {
			requests = extra
		}
	}

	// Preferred-garage boost for this owner
	if role == "garage_owner" && userID != "" && h.svc != nil {
		if preferred, e3 := h.svc.ListPendingForGarageOwner(c.Context(), userID, 50); e3 == nil {
			// merge preferred first
			seen := map[string]struct{}{}
			merged := make([]models.OpenServiceRequest, 0, len(preferred)+len(requests))
			for _, it := range preferred {
				seen[it.ID.String()] = struct{}{}
				merged = append(merged, it)
			}
			for _, it := range requests {
				if _, ok := seen[it.ID.String()]; !ok {
					merged = append(merged, it)
				}
			}
			requests = merged
		}
	}

	filtered := make([]models.OpenServiceRequest, 0, len(requests))
	for _, it := range requests {
		kind := strings.ToLower(strings.TrimSpace(it.RequestKind))
		if kind == "" {
			if it.Description != nil && strings.Contains(*it.Description, "[kind:garage_booking]") {
				kind = "garage_booking"
			} else {
				kind = "mechanic_request"
			}
			it.RequestKind = kind
		}
		switch role {
		case "mechanic":
			if kind == "mechanic_request" {
				filtered = append(filtered, it)
			}
		case "garage_owner":
			if kind == "garage_booking" {
				filtered = append(filtered, it)
			}
		default:
			// Unknown provider role — show all so demos still work
			filtered = append(filtered, it)
		}
	}

	return c.JSON(fiber.Map{
		"service_requests": filtered,
		"count":            len(filtered),
		"role":             role,
	})
}

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


// Cancel godoc
// @Summary      Cancel own pending/quoted service request
// @Tags         service-requests
// @Security     BearerAuth
// @Param        id   path      string  true  "Service Request ID"
// @Success      200  {object}  map[string]interface{}
// @Router       /service-requests/{id}/cancel [post]
func (h *ServiceRequestHandler) Cancel(c *fiber.Ctx) error {
	userIDStr, ok := middleware.UserID(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	ownerID, err := uuid.Parse(userIDStr)
	if err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, "invalid user id in token")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid id")
	}
	if err := h.svc.Cancel(c.Context(), id, ownerID); err != nil {
		return apierr.JSON(c, fiber.StatusConflict, "could not cancel — request may already be accepted or not yours")
	}
	return c.JSON(fiber.Map{"id": id, "status": "cancelled"})
}
