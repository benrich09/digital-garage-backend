package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
)

// bookingTransitions mirrors the booking_status enum's legal moves —
// separate from the service_request state machine since a booking's
// lifecycle (scheduled/in_progress/completed/cancelled) is simpler and
// nested inside the request's broader one.
var bookingTransitions = map[string][]string{
	"scheduled":   {"in_progress", "cancelled"},
	"in_progress": {"completed", "cancelled"},
}

type BookingService struct {
	bookings repository.BookingRepository
	requests repository.ServiceRequestRepository
	hub      *ws.Manager
	log      zerolog.Logger
}

func NewBookingService(bookings repository.BookingRepository, requests repository.ServiceRequestRepository, hub *ws.Manager, log zerolog.Logger) *BookingService {
	return &BookingService{bookings: bookings, requests: requests, hub: hub, log: log}
}

func (s *BookingService) GetByRequest(ctx context.Context, requestID uuid.UUID) (models.Booking, error) {
	return s.bookings.GetByServiceRequest(ctx, requestID)
}

// SetStatus transitions a booking (e.g. garage/mechanic marks the job
// started or completed) and notifies the car owner in real time.
func (s *BookingService) SetStatus(ctx context.Context, bookingID uuid.UUID, newStatus string) error {
	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("load booking: %w", err)
	}

	// Idempotent: already at target status
	if booking.Status == newStatus {
		return nil
	}

	allowed := bookingTransitions[booking.Status]
	ok := false
	for _, st := range allowed {
		if st == newStatus {
			ok = true
			break
		}
	}
	// Also allow scheduled -> completed (single-step finish)
	if !ok && booking.Status == "scheduled" && newStatus == "completed" {
		ok = true
	}
	if !ok {
		return fmt.Errorf("illegal booking transition: %s -> %s", booking.Status, newStatus)
	}

	if err := s.bookings.SetStatus(ctx, bookingID, newStatus); err != nil {
		return fmt.Errorf("update booking status: %w", err)
	}

	// Keep the parent service_request's status roughly in step so a car
	// owner polling GET /service-requests/mine sees consistent state,
	// not just the booking sub-resource.
	requestStatus := map[string]string{
		"in_progress": "in_progress",
		"completed":   "completed",
		"cancelled":   "cancelled",
	}[newStatus]
	if requestStatus != "" {
		_ = s.requests.UpdateStatus(ctx, booking.ServiceRequestID, requestStatus)
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	carOwnerID := uuid.Nil
	if err == nil {
		carOwnerID = req.CarOwnerID
	}

	// Reload so the WebSocket payload carries the freshly-stamped
	// started_at / completed_at (SetBookingStatus set them). This is what
	// lets the car owner run the same live service timer the provider
	// sees, and show the final duration when the job completes.
	updated, reloadErr := s.bookings.Get(ctx, bookingID)
	if reloadErr != nil {
		updated = booking
	}

	if carOwnerID != uuid.Nil {
		if newStatus == "completed" {
			s.hub.SendToUser(carOwnerID.String(), ws.NewEvent(ws.EventJobCompleted, ws.JobCompletedPayload{
				ServiceRequestID: booking.ServiceRequestID.String(),
				BookingID:        bookingID.String(),
				StartedAt:        updated.StartedAt,
				CompletedAt:      updated.CompletedAt,
			}))
		} else {
			s.hub.SendToUser(carOwnerID.String(), ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
				ServiceRequestID: booking.ServiceRequestID.String(),
				BookingID:        bookingID.String(),
				Status:           newStatus,
				StartedAt:        updated.StartedAt,
			}))
		}
	}

	s.log.Info().Str("booking_id", bookingID.String()).Str("to", newStatus).Msg("booking transitioned")
	return nil
}
