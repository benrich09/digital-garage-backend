package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
)

type ReviewRepository interface {
	Exists(ctx context.Context, bookingID, reviewerID uuid.UUID, garageID, mechanicID *uuid.UUID) (bool, error)
	Create(ctx context.Context, bookingID, reviewerID uuid.UUID, garageID, mechanicID *uuid.UUID, rating int32, comment *string) (uuid.UUID, error)
}

type reviewRepository struct {
	q *sqlcgen.Queries
}

func NewReviewRepository(q *sqlcgen.Queries) ReviewRepository {
	return &reviewRepository{q: q}
}

func (r *reviewRepository) Exists(ctx context.Context, bookingID, reviewerID uuid.UUID, garageID, mechanicID *uuid.UUID) (bool, error) {
	return r.q.ReviewExists(ctx, bookingID, reviewerID, garageID, mechanicID)
}

func (r *reviewRepository) Create(ctx context.Context, bookingID, reviewerID uuid.UUID, garageID, mechanicID *uuid.UUID, rating int32, comment *string) (uuid.UUID, error) {
	row, err := r.q.CreateReview(ctx, sqlcgen.CreateReviewParams{
		BookingID: bookingID, ReviewerID: reviewerID, GarageID: garageID, MechanicID: mechanicID,
		Rating: rating, Comment: comment,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}
