package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type BookingRepository interface {
	Get(ctx context.Context, id uuid.UUID) (models.Booking, error)
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
}

type bookingRepository struct {
	q *sqlcgen.Queries
}

func NewBookingRepository(q *sqlcgen.Queries) BookingRepository {
	return &bookingRepository{q: q}
}

func (r *bookingRepository) Get(ctx context.Context, id uuid.UUID) (models.Booking, error) {
	row, err := r.q.GetBooking(ctx, id)
	if err != nil {
		return models.Booking{}, err
	}
	return models.Booking{
		ID: row.ID, ServiceRequestID: row.ServiceRequestID, OfferID: row.OfferID,
		GarageID: row.GarageID, MechanicID: row.MechanicID, Status: row.Status, CreatedAt: row.CreatedAt,
	}, nil
}

func (r *bookingRepository) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.q.SetBookingStatus(ctx, id, status)
}
