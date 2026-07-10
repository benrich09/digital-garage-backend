package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
)

type MechanicService struct {
	mechanics repository.MechanicRepository
	bookings  repository.BookingRepository
	requests  repository.ServiceRequestRepository
	hub       *ws.Manager
	log       zerolog.Logger
}

func NewMechanicService(mechanics repository.MechanicRepository, bookings repository.BookingRepository, requests repository.ServiceRequestRepository, hub *ws.Manager, log zerolog.Logger) *MechanicService {
	return &MechanicService{mechanics: mechanics, bookings: bookings, requests: requests, hub: hub, log: log}
}

// UpdateLocation is hit periodically (e.g. every 5-15s) by the mechanic's
// Flutter app while a job is active. It updates the mechanic's live
// position (used for "track my mechanic" on the Google Map in the car
// owner's app), appends to the breadcrumb history table, and pushes a
// status_update event with the new coordinates so the car owner's map
// marker moves without polling.
func (s *MechanicService) UpdateLocation(ctx context.Context, mechanicProfileID uuid.UUID, bookingID uuid.UUID, lat, lng float64) error {
	mechanicID, err := s.mechanics.GetByProfileID(ctx, mechanicProfileID)
	if err != nil {
		return fmt.Errorf("resolve mechanic: %w", err)
	}

	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("load booking: %w", err)
	}
	if booking.MechanicID == nil || *booking.MechanicID != mechanicID {
		return fmt.Errorf("forbidden: not the mechanic assigned to this booking")
	}
	if booking.Status != "in_progress" && booking.Status != "scheduled" {
		return fmt.Errorf("booking is not active (status=%s)", booking.Status)
	}

	if err := s.mechanics.UpdateLocation(ctx, mechanicID, lat, lng); err != nil {
		return fmt.Errorf("update mechanic location: %w", err)
	}
	if err := s.mechanics.InsertLocationHistory(ctx, mechanicID, bookingID, lat, lng); err != nil {
		s.log.Warn().Err(err).Msg("failed to record location history (non-fatal)")
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err == nil {
		latCopy, lngCopy := lat, lng
		s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
			ServiceRequestID: booking.ServiceRequestID.String(),
			BookingID:        bookingID.String(),
			MechanicLat:      &latCopy,
			MechanicLng:      &lngCopy,
		}))
	}

	return nil
}
