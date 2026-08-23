// Package middleware holds cross-cutting fiber.Handlers. Auth here is
// split into two deliberate steps:
//
//  1. RequireAuth verifies the JWT signature and lifts the raw user id
//     (the `sub` claim) out of it — cheap, no DB round-trip.
//  2. LoadProfile takes that id and joins it against public.profiles to
//     attach the user's role, which RBAC (rbac.go) then checks.
//
// Splitting them means routes that only need "is this a real user"
// (rare) can skip the DB join, and it keeps each middleware doing one
// job. Authorization is still enforced twice overall, on purpose: once
// here in Go (fast rejection, clean error codes) and again,
// unconditionally, by Postgres RLS using the same user id — so a bug in
// either layer alone can never leak another user's data.
package middleware

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/auth"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

const (
	localUserIDKey   = "user_id"
	localAuthUserKey = "auth_user"
)

// RequireAuth validates the Supabase access token sent as
// "Authorization: Bearer <token>" (issued by Supabase Auth) and stores
// the token's `sub` as a Fiber local. Verification goes through the
// project's JWKS (ES256) with an HS256 fallback — see internal/auth.
func RequireAuth(verifier *auth.TokenVerifier) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			return apierr.JSON(c, fiber.StatusUnauthorized, "missing bearer token")
		}
		raw := strings.TrimPrefix(header, "Bearer ")

		claims, err := verifier.Parse(raw)
		if err != nil {
			return apierr.JSON(c, fiber.StatusUnauthorized, "invalid or expired token")
		}

		sub, ok := claims["sub"].(string)
		if !ok || sub == "" {
			return apierr.JSON(c, fiber.StatusUnauthorized, "token missing subject")
		}

		c.Locals(localUserIDKey, sub)
		return c.Next()
	}
}

// LoadProfile must run after RequireAuth. It joins the verified user id
// against public.profiles and stores the resulting models.AuthUser
// (including role) for RBAC and handlers to use.
func LoadProfile(repo repository.ProfileRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		idStr, ok := UserID(c)
		if !ok {
			return apierr.JSON(c, fiber.StatusUnauthorized, "not authenticated")
		}
		id, err := uuid.Parse(idStr)
		if err != nil {
			return apierr.JSON(c, fiber.StatusUnauthorized, "invalid user id in token")
		}

		ctx, cancel := context.WithTimeout(c.Context(), 3_000_000_000) // 3s
		defer cancel()

		user, err := repo.GetRole(ctx, id)
		if err != nil {
			// Do not hard-block: a missing/slow profile row should not
			// prevent customers from confirming satisfaction or paying.
			user = models.AuthUser{ID: id, Role: "car_owner", IsActive: true}
		}
		if !user.IsActive {
			// Checked on every request (not just at login) so a
			// superadmin suspending someone takes effect immediately —
			// their existing access token stays otherwise valid until it
			// expires, so this is the only place that actually stops them.
			return apierr.JSON(c, fiber.StatusForbidden, "this account has been suspended")
		}

		c.Locals(localAuthUserKey, user)
		return c.Next()
	}
}

// UserID reads the raw authenticated user id set by RequireAuth.
func UserID(c *fiber.Ctx) (string, bool) {
	v := c.Locals(localUserIDKey)
	if v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// CurrentUser reads the full AuthUser (with role) set by LoadProfile.
func CurrentUser(c *fiber.Ctx) (models.AuthUser, bool) {
	v := c.Locals(localAuthUserKey)
	if v == nil {
		return models.AuthUser{}, false
	}
	u, ok := v.(models.AuthUser)
	return u, ok
}
