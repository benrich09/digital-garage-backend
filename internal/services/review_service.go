package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
)

// Statuses that allow a rating after the job has been worked.
var reviewableRequestStatuses = map[string]bool{
	"completed": true, "paid": true, "closed": true, "finished": true,
	"done": true, "confirmed": true, "in_progress": true, "scheduled": true,
	"awaiting_payment": true, "payment_confirmed": true,
}

var reviewableBookingStatuses = map[string]bool{
	"completed": true, "paid": true, "closed": true, "finished": true,
	"done": true, "confirmed": true, "in_progress": true, "scheduled": true,
	"awaiting_payment": true, "payment_confirmed": true,
}

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

// Create allows mutual ratings:
//   - car_owner → garage or mechanic
//   - garage_owner / mechanic → car_owner
// Both sides may rate the same booking once each.
func (s *ReviewService) Create(ctx context.Context, callerID uuid.UUID, in models.CreateReviewInput) (uuid.UUID, error) {
	if in.Rating < 1 || in.Rating > 5 {
		return uuid.Nil, fmt.Errorf("rating must be between 1 and 5")
	}
	target := in.Target
	if target != "garage" && target != "mechanic" && target != "car_owner" {
		return uuid.Nil, fmt.Errorf(`target must be "garage", "mechanic", or "car_owner"`)
	}

	bookingID, err := uuid.Parse(in.BookingID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid booking_id")
	}

	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load booking: %w", err)
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load request: %w", err)
	}

	okStatus := reviewableRequestStatuses[req.Status] || reviewableBookingStatuses[booking.Status]
	if !okStatus {
		// Soft allow if booking exists and is past pending/quoted — mutual rating should not block.
		if req.Status == "pending" || req.Status == "quoted" || req.Status == "cancelled" {
			return uuid.Nil, fmt.Errorf("cannot review yet — finish the job first")
		}
	}

	isCarOwner := req.CarOwnerID == callerID
	isMechanic := booking.MechanicID != nil && *booking.MechanicID == callerID
	isGarageOwner := false
	if s.pool != nil && booking.GarageID != uuid.Nil {
		var ownerID uuid.UUID
		_ = s.pool.QueryRow(ctx, `select owner_id from garages where id = $1`, booking.GarageID).Scan(&ownerID)
		isGarageOwner = ownerID == callerID
	}
	// Fallback: provider apps often store provider user id as mechanic_id or only garage
	if !isCarOwner && !isMechanic && !isGarageOwner && s.pool != nil {
		// Any user linked as provider on service_transactions for this booking
		var n int
		_ = s.pool.QueryRow(ctx, `
			select count(*) from service_transactions
			where booking_id = $1 and provider_id = $2
		`, bookingID, callerID).Scan(&n)
		if n > 0 {
			isGarageOwner = true // treat as provider party
		}
	}

	isProvider := isMechanic || isGarageOwner
	if !isCarOwner && !isProvider {
		return uuid.Nil, fmt.Errorf("only the customer or the assigned provider can rate this job")
	}

	// Mutual rules
	switch target {
	case "car_owner":
		if isCarOwner {
			return uuid.Nil, fmt.Errorf("you cannot rate yourself")
		}
		if !isProvider {
			return uuid.Nil, fmt.Errorf("only the provider can rate the customer")
		}
	case "garage", "mechanic":
		if !isCarOwner {
			return uuid.Nil, fmt.Errorf("only the customer can rate the garage or mechanic")
		}
	}

	var garageID, mechanicID *uuid.UUID
	switch target {
	case "garage":
		if booking.GarageID == uuid.Nil {
			return uuid.Nil, fmt.Errorf("this booking has no garage to review")
		}
		g := booking.GarageID
		garageID = &g
	case "mechanic":
		if booking.MechanicID != nil {
			mechanicID = booking.MechanicID
		} else if booking.GarageID != uuid.Nil {
			g := booking.GarageID
			garageID = &g
			target = "garage"
		} else {
			return uuid.Nil, fmt.Errorf("this booking has no mechanic or garage to review")
		}
	case "car_owner":
		// Review of customer: null garage/mechanic; uniqueness is (booking, reviewer)
		garageID = nil
		mechanicID = nil
	}

	exists, err := s.reviews.Exists(ctx, bookingID, callerID, garageID, mechanicID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("check existing review: %w", err)
	}
	if exists {
		return uuid.Nil, fmt.Errorf("you have already submitted a rating for this job")
	}

	var comment *string
	if in.Comment != "" {
		comment = &in.Comment
	}

	id, err := s.reviews.Create(ctx, bookingID, callerID, garageID, mechanicID, in.Rating, comment)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create review: %w", err)
	}

	// Close job after a rating so it leaves Active lists (either side may rate first).
	_ = s.requests.UpdateStatus(ctx, booking.ServiceRequestID, "closed")
	_ = s.bookings.SetStatus(ctx, bookingID, "closed")
	return id, nil
}
