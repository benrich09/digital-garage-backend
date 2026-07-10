package handlers

import (
	"encoding/json"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type PaymentHandler struct {
	svc *services.PaymentService
}

func NewPaymentHandler(svc *services.PaymentService) *PaymentHandler {
	return &PaymentHandler{svc: svc}
}

// Initiate godoc
// @Summary      Initiate a mobile money payment for a completed booking
// @Description  car_owner-only. Triggers a Vodacom M-Pesa / Tigo Pesa / Airtel Money PIN prompt on phone_number via Flutterwave. Returns immediately with a pending status; final settlement arrives via the /webhooks/flutterwave callback.
// @Tags         payments
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      models.InitiatePaymentInput  true  "Payment details"
// @Success      202  {object}  map[string]interface{}
// @Failure      400  {object}  apierr.Response
// @Failure      403  {object}  apierr.Response
// @Router       /payments/initiate [post]
func (h *PaymentHandler) Initiate(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}

	var in models.InitiatePaymentInput
	if err := c.BodyParser(&in); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}
	if in.PhoneNumber == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "phone_number is required")
	}

	chargeResp, payment, err := h.svc.Initiate(c.Context(), user.ID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	resp := fiber.Map{
		"payment_id": payment.ID,
		"status":     "pending",
		"message":    "Check your phone to approve the mobile money payment.",
	}
	if chargeResp != nil {
		resp["provider_status"] = chargeResp.Data.Status
	}
	return c.Status(fiber.StatusAccepted).JSON(resp)
}

// flutterwaveWebhookPayload covers only the fields this app reads out
// of Flutterwave's webhook body — not a full representation of every
// field Flutterwave sends.
type flutterwaveWebhookPayload struct {
	Data struct {
		ID     int64  `json:"id"`
		TxRef  string `json:"tx_ref"`
		Status string `json:"status"`
	} `json:"data"`
}

// Webhook godoc
// @Summary      Flutterwave webhook callback
// @Description  Verifies the verif-hash header against the configured secret before trusting the payload. Not called by clients directly — configure this URL in the Flutterwave dashboard.
// @Tags         payments
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  apierr.Response
// @Router       /webhooks/flutterwave [post]
func (h *PaymentHandler) Webhook(c *fiber.Ctx) error {
	verifHash := c.Get("verif-hash")
	body := c.Body()

	var payload flutterwaveWebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.Data.TxRef == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "missing or malformed data.tx_ref in webhook payload")
	}

	err := h.svc.HandleWebhook(c.Context(), verifHash, body, payload.Data.TxRef, payload.Data.Status, strconv.FormatInt(payload.Data.ID, 10))
	if err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, err.Error())
	}

	return c.JSON(fiber.Map{"received": true})
}
