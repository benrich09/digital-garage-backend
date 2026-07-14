package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/internal/middleware"
	"github.com/yourorg/digital-garage/internal/services"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

type DeviceHandler struct {
	push *services.PushService
}

func NewDeviceHandler(push *services.PushService) *DeviceHandler {
	return &DeviceHandler{push: push}
}

type registerDeviceInput struct {
	Token    string `json:"token"`
	Platform string `json:"platform"` // "android" (this app is Android-only per the build)
}

// Register godoc
// @Summary      Register (or refresh) this device's FCM push token
// @Tags         devices
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      registerDeviceInput  true  "FCM token"
// @Success      200  {object}  map[string]interface{}
// @Router       /devices/register [post]
func (h *DeviceHandler) Register(c *fiber.Ctx) error {
	user, ok := middleware.CurrentUser(c)
	if !ok {
		return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
	}
	var in registerDeviceInput
	if err := c.BodyParser(&in); err != nil || in.Token == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "token is required")
	}
	if in.Platform == "" {
		in.Platform = "android"
	}
	if err := h.push.RegisterToken(c.Context(), user.ID, in.Token, in.Platform); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to register device token")
	}
	return c.JSON(fiber.Map{"status": "ok"})
}

type unregisterDeviceInput struct {
	Token string `json:"token"`
}

// Unregister godoc
// @Summary      Remove this device's FCM push token (e.g. on sign-out)
// @Tags         devices
// @Security     BearerAuth
// @Accept       json
// @Produce      json
// @Param        body  body      unregisterDeviceInput  true  "FCM token"
// @Success      200  {object}  map[string]interface{}
// @Router       /devices/unregister [post]
func (h *DeviceHandler) Unregister(c *fiber.Ctx) error {
	var in unregisterDeviceInput
	if err := c.BodyParser(&in); err != nil || in.Token == "" {
		return apierr.JSON(c, fiber.StatusBadRequest, "token is required")
	}
	if err := h.push.UnregisterToken(c.Context(), in.Token); err != nil {
		return apierr.JSON(c, fiber.StatusInternalServerError, "failed to unregister device token")
	}
	return c.JSON(fiber.Map{"status": "ok"})
}
