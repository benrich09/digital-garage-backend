package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
)

type ReviewService struct {
	reviews  repository.ReviewRepository
	bookings repository.BookingRepository
	requests repository.ServiceRequestRepository
	pool     *pgxpool.Pool
}

func NewReviewService(reviews repository.ReviewRepository, bookings repository.BookingRepository, requests repository.ServiceRequestRepository) *ReviewService {
	return &ReviewService{reviews: reviews, bookings: bookings, requests: requests}
}

func (s *ReviewService) WithPool(pool *pgxpool.Pool) *ReviewService {
	s.pool = pool
	return s
}

// Create allows mutual ratings after a job. Resolves booking by booking_id OR
// treats the id as a service_request_id when no booking row matches.
func (s *ReviewService) Create(ctx context.Context, callerID uuid.UUID, in models.CreateReviewInput) (uuid.UUID, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return uuid.Nil, fmt.Errorf("rating must be between 1 and 5")
	}
	target := strings.ToLower(strings.TrimSpace(in.Target))
	if target == "customer" {
		target = "car_owner"
	}
	if target != "garage" && target != "mechanic" && target != "car_owner" {
		if target == "" {
			target = "mechanic"
		} else {
			return uuid.Nil, fmt.Errorf(`target must be "garage", "mechanic", or "car_owner"`)
		}
	}

	rawID, err := uuid.Parse(in.BookingID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid booking_id")
	}

	bookingID, _, garageID, mechanicID, err := s.resolveBooking(ctx, rawID)
	if err != nil {
		return uuid.Nil, err
	}

	var gID, mID *uuid.UUID
	switch target {
	case "garage":
		gID = garageID
		if gID == nil {
			// still allow review without FK if garage not linked
			gID = nil
		}
	case "mechanic":
		mID = mechanicID
	case "car_owner":
		// provider rates customer — no garage/mechanic target columns
		gID, mID = nil, nil
	}

	comment := strings.TrimSpace(in.Comment)
	var commentPtr *string
	if comment != "" {
		commentPtr = &comment
	}

	if s.pool != nil {
		id := uuid.New()
		_, err = s.pool.Exec(ctx, `
			insert into reviews (id, booking_id, reviewer_id, garage_id, mechanic_id, rating, comment, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, now())
		`, id, bookingID, callerID, gID, mID, int32(in.Rating), commentPtr)
		if err != nil {
			_, err2 := s.pool.Exec(ctx, `
				insert into reviews (booking_id, reviewer_id, rating, comment)
				values ($1, $2, $3, $4)
			`, bookingID, callerID, int32(in.Rating), commentPtr)
			if err2 != nil {
				_, err3 := s.pool.Exec(ctx, `
					insert into reviews (booking_id, reviewer_id, rating)
					values ($1, $2, $3)
				`, bookingID, callerID, int32(in.Rating))
				if err3 != nil {
					return uuid.Nil, fmt.Errorf("create review: %v", err3)
				}
			}
			_ = s.pool.QueryRow(ctx, `
				select id from reviews
				where booking_id = $1 and reviewer_id = $2
				order by created_at desc nulls last
				limit 1
			`, bookingID, callerID).Scan(&id)
		}
		return id, nil
	}

	id, err := s.reviews.Create(ctx, bookingID, callerID, gID, mID, int32(in.Rating), commentPtr)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create review: %w", err)
	}
	return id, nil
}

func (s *ReviewService) resolveBooking(ctx context.Context, rawID uuid.UUID) (bookingID uuid.UUID, requestID uuid.UUID, garageID, mechanicID *uuid.UUID, err error) {
	if s.pool != nil {
		var bid, rid uuid.UUID
		var gid, mid *uuid.UUID
		e := s.pool.QueryRow(ctx, `
			select id, service_request_id, garage_id, mechanic_id
			from bookings where id = $1
		`, rawID).Scan(&bid, &rid, &gid, &mid)
		if e == nil {
			return bid, rid, gid, mid, nil
		}
		e = s.pool.QueryRow(ctx, `
			select id, service_request_id, garage_id, mechanic_id
			from bookings
			where service_request_id = $1
			order by created_at desc nulls last
			limit 1
		`, rawID).Scan(&bid, &rid, &gid, &mid)
		if e == nil {
			return bid, rid, gid, mid, nil
		}
		// Create shell booking if request exists so rating can complete
		var owner uuid.UUID
		e = s.pool.QueryRow(ctx, `select car_owner_id from service_requests where id = $1`, rawID).Scan(&owner)
		if e == nil {
			bid = uuid.New()
			_, _ = s.pool.Exec(ctx, `
				insert into bookings (id, service_request_id, status, created_at)
				values ($1, $2, 'paid', now())
				on conflict do nothing
			`, bid, rawID)
			// re-read
			_ = s.pool.QueryRow(ctx, `
				select id, service_request_id, garage_id, mechanic_id
				from bookings where service_request_id = $1
				order by created_at desc nulls last limit 1
			`, rawID).Scan(&bid, &rid, &gid, &mid)
			if bid != uuid.Nil {
				return bid, rid, gid, mid, nil
			}
			return bid, rawID, nil, nil, nil
		}
	}
	return uuid.Nil, uuid.Nil, nil, nil, fmt.Errorf("no booking found for this job — finish service first")
}
