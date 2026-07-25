package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type CommissionHandler struct {
	svc *services.CommissionService
}

func NewCommissionHandler(svc *services.CommissionService) *CommissionHandler {
	return &CommissionHandler{svc: svc}
}

// RecordService godoc
// @Summary      Record a completed job you were paid for directly
// @Description  Provider-only. Creates a transaction awaiting the car owner's confirmation. No commission is booked until they confirm.
// @Tags         commission
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      models.RecordServiceInput  true  "Job details"
// @Success      201  {object}  models.ServiceTransaction
// @Failure      400  {object}  apierr.Response
// @Router       /transactions [post]
func (h *CommissionHandler) RecordService(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}

	var in models.RecordServiceInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	txn, err := h.svc.RecordService(c.Context(), user.ID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(txn)
}

// Confirm godoc
// @Summary      Confirm you paid the provider
// @Description  car_owner-only. This is what books the platform's 5% commission — a provider cannot confirm their own sale.
// @Tags         commission
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      string  true  "Transaction ID"
// @Success      200  {object}  models.ServiceTransaction
// @Failure      403  {object}  apierr.Response
// @Router       /transactions/{id}/confirm [post]
func (h *CommissionHandler) Confirm(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid transaction id")
	}

	txn, err := h.svc.Confirm(c.Context(), user.ID, id)
	if err != nil {
		// Wrong caller is a 403; everything else here is a state problem.
		if err == services.ErrNotCarOwner {
			return apierr.JSON(c, fiber.StatusForbidden, err.Error())
		}
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.JSON(txn)
}

// Dispute godoc
// @Summary      Report that you did not pay for a recorded job
// @Tags         commission
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path  string               true  "Transaction ID"
// @Param        body  body  models.DisputeInput  true  "Reason"
// @Success      204
// @Router       /transactions/{id}/dispute [post]
func (h *CommissionHandler) Dispute(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid transaction id")
	}

	var in models.DisputeInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if in.Reason == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "reason is required")
	}

	if err := h.svc.Dispute(c.Context(), user.ID, id, in.Reason); err != nil {
		if err == services.ErrNotCarOwner {
			return apierr.JSON(c, fiber.StatusForbidden, err.Error())
		}
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// MyBalance godoc
// @Summary      What you currently owe the platform
// @Description  Provider-only. Drives the home dashboard.
// @Tags         commission
// @Security     BearerAuth
// @Produce      json
// @Success      200  {object}  services.Balance
// @Router       /providers/me/balance [get]
func (h *CommissionHandler) MyBalance(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	b, err := h.svc.BalanceFor(c.Context(), user.ID)
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(b)
}

// SubmitSettlement godoc
// @Summary      Report that you paid your monthly commission
// @Description  Provider-only. Does not clear the debt — an admin must verify the reference first.
// @Tags         commission
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        id    path  string                        true  "Settlement ID"
// @Param        body  body  models.SubmitSettlementInput  true  "Payment reference"
// @Success      204
// @Router       /settlements/{id}/submit [post]
func (h *CommissionHandler) SubmitSettlement(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid settlement id")
	}

	var in models.SubmitSettlementInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	if err := h.svc.SubmitSettlement(c.Context(), user.ID, id, in.Reference, in.Method); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// VerifySettlement godoc
// @Summary      Verify a provider's settlement payment
// @Description  Admin-only. Writes the ledger credit that actually clears the debt.
// @Tags         admin
// @Security     BearerAuth
// @Produce      json
// @Param        id  path  string  true  "Settlement ID"
// @Success      204
// @Router       /admin/settlements/{id}/verify [post]
func (h *CommissionHandler) VerifySettlement(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "invalid settlement id")
	}

	if err := h.svc.VerifySettlement(c.Context(), user.ID, id); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// ListDebtors godoc
// @Summary      Providers who owe the platform money
// @Tags         admin
// @Security     BearerAuth
// @Produce      json
// @Success      200  {array}  models.LedgerBalance
// @Router       /admin/debts [get]
func (h *CommissionHandler) ListDebtors(c *fiber.Ctx) error {
	list, err := h.svc.ListDebtors(c.Context())
	if err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, err.Error())
	}
	return c.JSON(list)
}
