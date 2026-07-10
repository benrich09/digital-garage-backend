// Package apierr defines one consistent JSON error shape for every
// handler, so mobile/web clients parse errors the same way everywhere.
package apierr

import "github.com/gofiber/fiber/v2"

type Response struct {
	Error string `json:"error"`
}

func JSON(c *fiber.Ctx, status int, message string) error {
	return c.Status(status).JSON(Response{Error: message})
}
