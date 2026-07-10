package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
)

type MechanicRepository interface {
	GetByProfileID(ctx context.Context, profileID uuid.UUID) (uuid.UUID, error) // returns mechanic id
	UpdateLocation(ctx context.Context, mechanicID uuid.UUID, lat, lng float64) error
	InsertLocationHistory(ctx context.Context, mechanicID, bookingID uuid.UUID, lat, lng float64) error
}

type mechanicRepository struct {
	q *sqlcgen.Queries
}

func NewMechanicRepository(q *sqlcgen.Queries) MechanicRepository {
	return &mechanicRepository{q: q}
}

func (r *mechanicRepository) GetByProfileID(ctx context.Context, profileID uuid.UUID) (uuid.UUID, error) {
	row, err := r.q.GetMechanicByProfileID(ctx, profileID)
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

func (r *mechanicRepository) UpdateLocation(ctx context.Context, mechanicID uuid.UUID, lat, lng float64) error {
	return r.q.UpdateMechanicLocation(ctx, mechanicID, lng, lat)
}

func (r *mechanicRepository) InsertLocationHistory(ctx context.Context, mechanicID, bookingID uuid.UUID, lat, lng float64) error {
	return r.q.InsertMechanicLocationHistory(ctx, mechanicID, bookingID, lng, lat)
}
