package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
)

// Statuses that allow a rating. Includes common variants used across apps.
var reviewableRequestStatuses = map[string]bool{
	"completed": true, "paid": true, "closed": true, "finished": true,
	"done": true, "confirmed": true, "in_progress": true, // allow after finish flow even if lag
}

var reviewableBookingStatuses = map[string]bool{
	"completed": true, "paid": true, "closed": true, "finished": true,
	"done": true, "confirmed": true, "in_progress": true, "scheduled": true,
}

type ReviewService struct {
	reviews  repository.ReviewRepository
	bookings repository.BookingRepository
	requests repository.ServiceRequestRepository
}

func NewReviewService(reviews repository.ReviewRepository, bookings repository.BookingRepository, requests repository.ServiceRequestRepository) *ReviewService {
	return &ReviewService{reviews: reviews, bookings: bookings, requests: requests}
}

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

	// Allow rating once the job has progressed far enough (finish/pay flow).
	okStatus := reviewableRequestStatuses[req.Status] || reviewableBookingStatuses[booking.Status]
	if !okStatus {
		return uuid.Nil, fmt.Errorf("cannot review yet (request=%s booking=%s) — finish the job first", req.Status, booking.Status)
	}

	isOwner := req.CarOwnerID == callerID
	// Providers rate car_owner; owners rate garage/mechanic.
	if target == "car_owner" && isOwner {
		return uuid.Nil, fmt.Errorf("car owners rate the garage or mechanic, not themselves")
	}
	if (target == "garage" || target == "mechanic") && !isOwner {
		// Provider rating the other side is only car_owner target.
		// Soft: still allow provider to rate garage/mechanic for self-jobs is wrong — block.
		return uuid.Nil, fmt.Errorf("providers rate the customer (target=car_owner)")
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
		if booking.MechanicID == nil {
			// Fall back to garage when no mechanic row (garage-only job).
			if booking.GarageID == uuid.Nil {
				return uuid.Nil, fmt.Errorf("this booking has no mechanic or garage to review")
			}
			g := booking.GarageID
			garageID = &g
			target = "garage"
		} else {
			mechanicID = booking.MechanicID
		}
	case "car_owner":
		// Stored as a review with null garage/mechanic — identified by reviewer=provider.
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
	// Close the job so it leaves Active lists for both apps.
	_ = s.requests.UpdateStatus(ctx, booking.ServiceRequestID, "closed")
	_ = s.bookings.SetStatus(ctx, bookingID, "closed")
	return id, nil
}
