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

// Create supports mutual ratings after migration 0020:
// car_owner → garage/mechanic; provider → car_owner.
func (s *ReviewService) Create(ctx context.Context, callerID uuid.UUID, in models.CreateReviewInput) (uuid.UUID, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return uuid.Nil, fmt.Errorf("rating must be between 1 and 5")
	}
	target := strings.ToLower(strings.TrimSpace(in.Target))
	if target == "customer" || target == "owner" {
		target = "car_owner"
	}
	if target == "" {
		target = "mechanic"
	}

	rawID, err := uuid.Parse(strings.TrimSpace(in.BookingID))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid booking_id")
	}
	if s.pool == nil {
		return uuid.Nil, fmt.Errorf("database unavailable")
	}

	bookingID, requestID, garageID, mechanicID, carOwnerID, err := s.resolveBooking(ctx, rawID)
	if err != nil {
		return uuid.Nil, err
	}

	var gID, mID, cID *uuid.UUID
	switch target {
	case "garage":
		gID = garageID
	case "mechanic":
		mID = mechanicID
		if mID == nil && gID == nil {
			gID = garageID // fallback
		}
	case "car_owner":
		cID = carOwnerID
	}

	comment := strings.TrimSpace(in.Comment)
	var commentPtr *string
	if comment != "" {
		commentPtr = &comment
	}

	id := uuid.New()
	_, err = s.pool.Exec(ctx, `
		insert into reviews (id, booking_id, reviewer_id, garage_id, mechanic_id, car_owner_id, rating, comment, created_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8, now())
	`, id, bookingID, callerID, gID, mID, cID, int32(in.Rating), commentPtr)
	if err != nil {
		// Without car_owner_id column (pre-0020)
		_, err = s.pool.Exec(ctx, `
			insert into reviews (id, booking_id, reviewer_id, garage_id, mechanic_id, rating, comment, created_at)
			values ($1,$2,$3,$4,$5,$6,$7, now())
		`, id, bookingID, callerID, gID, mID, int32(in.Rating), commentPtr)
		if err != nil {
			_, err = s.pool.Exec(ctx, `
				insert into reviews (booking_id, reviewer_id, rating, comment)
				values ($1,$2,$3,$4)
			`, bookingID, callerID, int32(in.Rating), commentPtr)
			if err != nil {
				return uuid.Nil, fmt.Errorf("create review: %v", err)
			}
		}
	}

	_, _ = s.pool.Exec(ctx, `
		update bookings set status = case
			when status in ('paid','customer_claims_paid','awaiting_payment','completed') then 'closed'
			else status end,
			updated_at = now()
		where id = $1
	`, bookingID)
	_ = requestID
	return id, nil
}

func (s *ReviewService) resolveBooking(ctx context.Context, rawID uuid.UUID) (
	bookingID, requestID uuid.UUID, garageID, mechanicID, carOwnerID *uuid.UUID, err error,
) {
	var bid, rid uuid.UUID
	var gid, mid *uuid.UUID
	var owner uuid.UUID
	e := s.pool.QueryRow(ctx, `
		select b.id, b.service_request_id, b.garage_id, b.mechanic_id, sr.car_owner_id
		from bookings b
		join service_requests sr on sr.id = b.service_request_id
		where b.id = $1
	`, rawID).Scan(&bid, &rid, &gid, &mid, &owner)
	if e != nil {
		e = s.pool.QueryRow(ctx, `
			select b.id, b.service_request_id, b.garage_id, b.mechanic_id, sr.car_owner_id
			from bookings b
			join service_requests sr on sr.id = b.service_request_id
			where b.service_request_id = $1
			order by b.created_at desc nulls last
			limit 1
		`, rawID).Scan(&bid, &rid, &gid, &mid, &owner)
	}
	if e != nil {
		// shell booking
		if e2 := s.pool.QueryRow(ctx, `select car_owner_id from service_requests where id = $1`, rawID).Scan(&owner); e2 != nil {
			return uuid.Nil, uuid.Nil, nil, nil, nil, fmt.Errorf("no booking found for this job")
		}
		bid = uuid.New()
		_, _ = s.pool.Exec(ctx, `
			insert into bookings (id, service_request_id, status, created_at)
			values ($1, $2, 'paid', now())
		`, bid, rawID)
		rid = rawID
	}
	co := owner
	return bid, rid, gid, mid, &co, nil
}
