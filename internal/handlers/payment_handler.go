package handlers

import (
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
	"github.com/yourorg/digital-garage/pkg/mpesa"
	"github.com/yourorg/digital-garage/pkg/selcom"
)

type PaymentHandler struct {
	svc                 *services.PaymentService
	mpesaCallbackSecret string
}

func NewPaymentHandler(svc *services.PaymentService, mpesaCallbackSecret string) *PaymentHandler {
	return &PaymentHandler{svc: svc, mpesaCallbackSecret: mpesaCallbackSecret}
}

// Initiate godoc
// @Summary      Initiate a mobile money payment for a completed booking
// @Description  car_owner-only. Triggers an M-Pesa or Selcom PIN prompt on phone_number, selected via the "provider" field. Returns immediately with a pending status; final settlement arrives via /webhooks/mpesa or /webhooks/selcom.
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

	result, err := h.svc.Initiate(c.Context(), user.ID, in)
	if err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, err.Error())
	}

	resp := fiber.Map{
		"payment_id": result.Payment.ID,
		"provider":   result.Payment.Provider,
		"status":     "pending",
		"message":    "Check your phone to approve the mobile money payment.",
	}
	if result.CustomerMessage != "" {
		resp["provider_message"] = result.CustomerMessage
	}
	if result.ProviderStatus != "" {
		resp["provider_status"] = result.ProviderStatus
	}
	return c.Status(fiber.StatusAccepted).JSON(resp)
}

// MpesaCallback godoc
// @Summary      M-Pesa (Daraja) STK Push callback
// @Description  Not called by clients directly — configure this URL (with the shared secret query param) as CallBackURL on Daraja.
// @Tags         payments
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  apierr.Response
// @Router       /webhooks/mpesa [post]
func (h *PaymentHandler) MpesaCallback(c *fiber.Ctx) error {
	if !mpesa.VerifyCallbackSecret(h.mpesaCallbackSecret, c.Query("secret")) {
		return apierr.JSON(c, fiber.StatusUnauthorized, "invalid or missing callback secret")
	}

	var payload mpesa.CallbackPayload
	if err := json.Unmarshal(c.Body(), &payload); err != nil {
		return apierr.JSON(c, fiber.StatusBadRequest, "malformed callback payload")
	}

	if err := h.svc.HandleMpesaCallback(c.Context(), payload); err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, err.Error())
	}
	return c.JSON(fiber.Map{"received": true})
}

// SelcomWebhook godoc
// @Summary      Selcom payment status webhook
// @Description  Verifies the Digest header against the configured API secret before trusting the payload. Not called by clients directly — configure this URL in the Selcom merchant dashboard.
// @Tags         payments
// @Accept       json
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  apierr.Response
// @Router       /webhooks/selcom [post]
func (h *PaymentHandler) SelcomWebhook(c *fiber.Ctx) error {
	digest := c.Get("Digest")
	body := c.Body()

	var payload selcom.WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil || payload.OrderID == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "missing or malformed order_id in webhook payload")
	}

	if err := h.svc.HandleSelcomWebhook(c.Context(), digest, body, payload); err != nil {
		return apierr.JSON(c, fiber.StatusUnauthorized, err.Error())
	}
	return c.JSON(fiber.Map{"received": true})
}
