package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
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

// SetStatus transitions a booking (e.g. garage/mechanic marks the job
// started or completed) and notifies the car owner in real time.
func (s *BookingService) SetStatus(ctx context.Context, bookingID uuid.UUID, newStatus string) error {
	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("load booking: %w", err)
	}

	allowed := bookingTransitions[booking.Status]
	ok := false
	for _, st := range allowed {
		if st == newStatus {
			ok = true
			break
		}
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

	evtType := ws.EventStatusUpdate
	if newStatus == "completed" {
		evtType = ws.EventJobCompleted
	}

	if carOwnerID != uuid.Nil {
		s.hub.SendToUser(carOwnerID.String(), ws.NewEvent(evtType, ws.StatusUpdatePayload{
			ServiceRequestID: booking.ServiceRequestID.String(),
			BookingID:        bookingID.String(),
			Status:           newStatus,
		}))
	}

	s.log.Info().Str("booking_id", bookingID.String()).Str("to", newStatus).Msg("booking transitioned")
	return nil
}
