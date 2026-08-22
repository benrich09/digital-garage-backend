package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type JobHandler struct {
	svc *services.JobLifecycleService
}

func NewJobHandler(svc *services.JobLifecycleService) *JobHandler {
	return &JobHandler{svc: svc}
}

func (h *JobHandler) Snapshot(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	snap, err := h.svc.GetSnapshot(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusNotFound, "booking not found")
	}
	return c.JSON(snap)
}

func (h *JobHandler) ConfirmArrival(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	role := "car_owner"
	if u, ok := middleware.CurrentUser(c); ok {
		role = u.Role
	}
	snap, err := h.svc.ConfirmArrival(c.Context(), id, role)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

func (h *JobHandler) Start(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	snap, err := h.svc.StartService(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

func (h *JobHandler) Finish(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	snap, err := h.svc.FinishService(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

func (h *JobHandler) ConfirmSatisfaction(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	snap, err := h.svc.ConfirmSatisfaction(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

type setBillInput struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

func (h *JobHandler) SetBill(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	var in setBillInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid body")
	}
	snap, err := h.svc.SetBill(c.Context(), id, in.Amount, in.Currency)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

func (h *JobHandler) CustomerPaid(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	snap, err := h.svc.CustomerMarksPaid(c.Context(), id)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}

type confirmPayInput struct {
	Received bool `json:"received"`
}

func (h *JobHandler) ConfirmPayment(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid booking id")
	}
	var in confirmPayInput
	_ = c.BodyParser(&in)
	// default true if body empty
	if c.Body() == nil || len(c.Body()) == 0 {
		in.Received = true
	}
	snap, err := h.svc.ProviderConfirmPayment(c.Context(), id, in.Received)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(snap)
}
