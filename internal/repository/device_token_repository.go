package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
)

type DeviceTokenRepository interface {
	Register(ctx context.Context, userID uuid.UUID, token, platform string) error
	Unregister(ctx context.Context, token string) error
	ListForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
}

type deviceTokenRepository struct {
	q *sqlcgen.Queries
}

func NewDeviceTokenRepository(q *sqlcgen.Queries) DeviceTokenRepository {
	return &deviceTokenRepository{q: q}
}

func (r *deviceTokenRepository) Register(ctx context.Context, userID uuid.UUID, token, platform string) error {
	return r.q.UpsertDeviceToken(ctx, userID, token, platform)
}

func (r *deviceTokenRepository) Unregister(ctx context.Context, token string) error {
	return r.q.DeleteDeviceToken(ctx, token)
}

func (r *deviceTokenRepository) ListForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	return r.q.ListDeviceTokensForUser(ctx, userID)
}
