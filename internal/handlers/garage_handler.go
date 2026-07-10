package handlers

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type GarageHandler struct {
	svc *services.GarageService
}

func NewGarageHandler(svc *services.GarageService) *GarageHandler {
	return &GarageHandler{svc: svc}
}

// ListNearby godoc
// @Summary      Find nearby approved garages
// @Description  Returns approved, active garages within radius_km of (lat, lng), closest first. lat/lng in the response are plain floats, directly usable by the Google Maps SDK.
// @Tags         garages
// @Produce      json
// @Param        lat        query     number  true   "Latitude"
// @Param        lng        query     number  true   "Longitude"
// @Param        radius_km  query     number  false  "Search radius in km (default 5)"
// @Param        limit      query     int     false  "Max results (default 20, max 50)"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      500  {object}  apierr.Response
// @Router       /garages/nearby [get]
func (h *GarageHandler) ListNearby(c *fiber.Ctx) error {
	lat, err := strconv.ParseFloat(c.Query("lat"), 64)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "lat is required and must be a number")
	}
	lng, err := strconv.ParseFloat(c.Query("lng"), 64)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "lng is required and must be a number")
	}
	radiusKM, err := strconv.ParseFloat(c.Query("radius_km", "5"), 64)
	if err != nil || radiusKM <= 0 {
		radiusKM = 5
	}
	limit, _ := strconv.Atoi(c.Query("limit", "20"))

	garages, err := h.svc.FindNearby(c.Context(), lat, lng, radiusKM, int32(limit))
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to search garages")
	}

	return c.JSON(fiber.Map{"garages": garages})
}

// SubmitVerification godoc
// @Summary      Submit garage business verification
// @Description  garage_owner-only. Creates the garage record in "pending" status with business details; invisible to car owners until an admin approves it.
// @Tags         garages
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      models.GarageVerificationInput  true  "Garage business details"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      401  {object}  apierr.Response
// @Failure      403  {object}  apierr.Response
// @Router       /garage-owner/verify [post]
func (h *GarageHandler) SubmitVerification(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}

	var in models.GarageVerificationInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if in.Name == "" || in.LicenseNumber == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "name and license_number are required")
	}

	garageID, err := h.svc.SubmitVerification(c.Context(), user.ID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to submit garage verification")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"garage_id": garageID,
		"status":    "pending",
	})
}
