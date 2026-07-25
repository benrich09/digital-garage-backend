package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type ProfileRepository interface {
	GetRole(ctx context.Context, id uuid.UUID) (models.AuthUser, error)
}

type profileRepository struct {
	q *sqlcgen.Queries
}

func NewProfileRepository(q *sqlcgen.Queries) ProfileRepository {
	return &profileRepository{q: q}
}

func (r *profileRepository) GetRole(ctx context.Context, id uuid.UUID) (models.AuthUser, error) {
	row, err := r.q.GetProfileRole(ctx, id)
	if err != nil {
		return models.AuthUser{}, err
	}
	return models.AuthUser{ID: row.ID, Role: row.Role, FullName: row.FullName, IsActive: row.IsActive}, nil
}
