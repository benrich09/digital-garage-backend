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
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/pkg/apierr"
)

const (
	localUserIDKey   = "user_id"
	localAuthUserKey = "auth_user"
)

// RequireAuth validates the Supabase access token sent as
// "Authorization: Bearer <token>" (issued by Supabase Auth after
// phone/OTP verification) and stores the token's `sub` as a Fiber local.
func RequireAuth(jwtSecret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		header := c.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			return apierr.JSON(c, fiber.StatusUnauthorized, "missing bearer token")
		}
		raw := strings.TrimPrefix(header, "Bearer ")

		claims := jwt.MapClaims{}
		token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return []byte(jwtSecret), nil
		})
		if err != nil || !token.Valid {
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
			return apierr.JSON(c, fiber.StatusForbidden, "profile not found")
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
