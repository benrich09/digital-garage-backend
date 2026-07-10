package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
)

// completedRequestStatuses gates reviews: a car owner may only review
// once the job has actually finished, not while it's still pending,
// quoted, accepted, or in progress.
var completedRequestStatuses = map[string]bool{
	"completed": true,
	"paid":      true,
	"closed":    true,
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
	if in.Target != "garage" && in.Target != "mechanic" {
		return uuid.Nil, fmt.Errorf(`target must be "garage" or "mechanic"`)
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
	// The one rule this whole handler exists to enforce: you cannot
	// review a request you weren't the car owner on, or that never
	// actually completed.
	if req.CarOwnerID != callerID {
		return uuid.Nil, fmt.Errorf("forbidden: you did not create this request")
	}
	if !completedRequestStatuses[req.Status] {
		return uuid.Nil, fmt.Errorf("cannot review a request that hasn't been completed (status=%s)", req.Status)
	}

	var garageID, mechanicID *uuid.UUID
	if in.Target == "garage" {
		garageID = &booking.GarageID
	} else {
		if booking.MechanicID == nil {
			return uuid.Nil, fmt.Errorf("this booking has no assigned mechanic to review")
		}
		mechanicID = booking.MechanicID
	}

	exists, err := s.reviews.Exists(ctx, bookingID, callerID, garageID, mechanicID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("check existing review: %w", err)
	}
	if exists {
		return uuid.Nil, fmt.Errorf("you have already reviewed this %s for this booking", in.Target)
	}

	var comment *string
	if in.Comment != "" {
		comment = &in.Comment
	}

	id, err := s.reviews.Create(ctx, bookingID, callerID, garageID, mechanicID, in.Rating, comment)
	if err != nil {
		return uuid.Nil, fmt.Errorf("create review: %w", err)
	}
	return id, nil
}
