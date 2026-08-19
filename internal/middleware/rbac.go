package middleware

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

// RequireRole must run after RequireAuth + LoadProfile. It's a plain
// allow-list check — no hierarchy, no inheritance.
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
		role := normalizeRole(user.Role)
		if _, permitted := allowed[role]; !permitted {
			return apierr.JSON(c, fiber.StatusForbidden,
				fmt.Sprintf("role not permitted for this action (your role=%q; need one of %v). In Supabase SQL: update profiles set role='mechanic' where id=auth.uid();",
					user.Role, roles))
		}
		if role != user.Role {
			user.Role = role
			c.Locals(localAuthUserKey, user)
		}
		return c.Next()
	}
}

func normalizeRole(role string) string {
	switch role {
	case "Garage Owner", "garage-owner", "GarageOwner":
		return "garage_owner"
	case "Mechanic", "MECHANIC":
		return "mechanic"
	case "Car Owner", "car-owner", "CarOwner":
		return "car_owner"
	case "Admin", "ADMIN":
		return "admin"
	case "Super Admin", "super-admin", "SuperAdmin":
		return "superadmin"
	default:
		return role
	}
}
