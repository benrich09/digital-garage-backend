package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type ReviewHandler struct {
	svc *services.ReviewService
}

func NewReviewHandler(svc *services.ReviewService) *ReviewHandler {
	return &ReviewHandler{svc: svc}
}

// Create godoc
// @Summary      Rate a garage or mechanic after job completion
// @Description  car_owner-only. Rejects reviews on requests the caller didn't create or that haven't completed yet, and rejects duplicate reviews for the same booking/target.
// @Tags         reviews
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      models.CreateReviewInput  true  "Review details"
// @Success      201  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      403  {object}  apierr.Response
// @Router       /reviews [post]
func (h *ReviewHandler) Create(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}

	var in models.CreateReviewInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	id, err := h.svc.Create(c.Context(), user.ID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": id})
}
