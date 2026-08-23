package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

// RequireRole must run after RequireAuth + LoadProfile.
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
			// Never leak role names / SQL to end users — superadmin sees logs server-side.
			return apierr.JSON(c, fiber.StatusForbidden, "You cannot do that from this account.")
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
	case "Garage Owner", "garage-owner", "GarageOwner", "garageowner", "GARAGE_OWNER":
		return "garage_owner"
	case "Mechanic", "MECHANIC", "mechanic":
		return "mechanic"
	case "Car Owner", "car-owner", "CarOwner", "customer", "Customer", "owner", "CAR_OWNER":
		return "car_owner"
	case "Admin", "ADMIN":
		return "admin"
	case "Super Admin", "super-admin", "SuperAdmin", "super_admin":
		return "superadmin"
	default:
		// already snake_case or unknown
		if role == "car_owner" || role == "mechanic" || role == "garage_owner" || role == "admin" || role == "superadmin" {
			return role
		}
		return role
	}
}
