package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

// RequireRole must run after RequireAuth + LoadProfile. It's a plain
// allow-list check — no hierarchy, no inheritance — since the four
// roles here (car_owner, garage_owner, mechanic, admin) don't nest into
// each other; a garage_owner isn't "more" than a mechanic, they're just
// different actors in the marketplace. admin is never implicitly
// granted by other roles — routes that admins should also reach must
// list "admin" explicitly.
func RequireRole(roles ...string) fiber.Handler {
	allowed := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *fiber.Ctx) error {
		user, ok := CurrentUser(c)
		if !ok {
			return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
		}
		if _, permitted := allowed[user.Role]; !permitted {
			return apierr.JSON(c, fiber.StatusForbidden, "role not permitted for this action")
		}
		return c.Next()
	}
}
