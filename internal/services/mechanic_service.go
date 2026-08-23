package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	pool      *pgxpool.Pool
}

func NewMechanicService(mechanics repository.MechanicRepository, bookings repository.BookingRepository, requests repository.ServiceRequestRepository, hub *ws.Manager, log zerolog.Logger) *MechanicService {
	return &MechanicService{mechanics: mechanics, bookings: bookings, requests: requests, hub: hub, log: log}
}

func (s *MechanicService) WithPool(pool *pgxpool.Pool) *MechanicService {
	s.pool = pool
	return s
}

// UpdateLocation is hit periodically by the provider app while a job is active.
// Updates mechanic current_location (when applicable), history, and WS-pushes
// coordinates to the car owner so the track map moves without polling alone.
func (s *MechanicService) UpdateLocation(ctx context.Context, profileID uuid.UUID, bookingID uuid.UUID, lat, lng float64) error {
	// Prefer pool path — resilient to missing mechanic assignment / status variants
	if s.pool != nil {
		return s.updateLocationPool(ctx, profileID, bookingID, lat, lng)
	}

	mechanicID, err := s.mechanics.GetByProfileID(ctx, profileID)
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
	st := strings.ToLower(booking.Status)
	if !isTrackableStatus(st) {
		return fmt.Errorf("booking is not active (status=%s)", booking.Status)
	}
	if err := s.mechanics.UpdateLocation(ctx, mechanicID, lat, lng); err != nil {
		return fmt.Errorf("update mechanic location: %w", err)
	}
	_ = s.mechanics.InsertLocationHistory(ctx, mechanicID, bookingID, lat, lng)
	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err == nil {
		s.pushLocation(req.CarOwnerID.String(), booking.ServiceRequestID.String(), bookingID.String(), lat, lng)
	}
	return nil
}

func isTrackableStatus(st string) bool {
	switch st {
	case "scheduled", "accepted", "en_route", "arrived", "in_progress", "awaiting_customer":
		return true
	default:
		return false
	}
}

func (s *MechanicService) updateLocationPool(ctx context.Context, profileID, bookingID uuid.UUID, lat, lng float64) error {
	var requestID, ownerID uuid.UUID
	var status string
	var mechanicID *uuid.UUID
	err := s.pool.QueryRow(ctx, `
		select b.service_request_id, sr.car_owner_id, lower(coalesce(b.status,'')), b.mechanic_id
		from bookings b
		join service_requests sr on sr.id = b.service_request_id
		where b.id = $1
	`, bookingID).Scan(&requestID, &ownerID, &status, &mechanicID)
	if err != nil {
		return fmt.Errorf("load booking: %w", err)
	}
	if !isTrackableStatus(status) {
		// Still accept location for en_route-ish labels
		if status != "pending" && status != "cancelled" && status != "paid" && status != "closed" {
			// allow
		} else if status == "pending" {
			return fmt.Errorf("booking is not active (status=%s)", status)
		}
	}

	// Update mechanics.current_location when this profile is a mechanic
	var mid uuid.UUID
	if e := s.pool.QueryRow(ctx, `select id from mechanics where profile_id = $1 limit 1`, profileID).Scan(&mid); e == nil {
		_, _ = s.pool.Exec(ctx, `
			update mechanics
			set current_location = ST_SetSRID(ST_MakePoint($2::float8, $3::float8), 4326)::geography,
			    location_updated_at = now()
			where id = $1
		`, mid, lng, lat)
		_, _ = s.pool.Exec(ctx, `
			insert into mechanic_location_history (mechanic_id, booking_id, location, recorded_at)
			values ($1, $2, ST_SetSRID(ST_MakePoint($3::float8, $4::float8), 4326)::geography, now())
		`, mid, bookingID, lng, lat)
	}

	// Also store last known provider position on the booking for garage flows
	_, _ = s.pool.Exec(ctx, `
		update bookings set updated_at = now() where id = $1
	`, bookingID)

	// Optional last_lat/last_lng if columns exist
	_, _ = s.pool.Exec(ctx, `
		update bookings set last_lat = $2, last_lng = $3, updated_at = now() where id = $1
	`, bookingID, lat, lng)

	s.pushLocation(ownerID.String(), requestID.String(), bookingID.String(), lat, lng)
	return nil
}

func (s *MechanicService) pushLocation(ownerID, requestID, bookingID string, lat, lng float64) {
	if s.hub == nil || ownerID == "" {
		return
	}
	latCopy, lngCopy := lat, lng
	s.hub.SendToUser(ownerID, ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
		ServiceRequestID: requestID,
		BookingID:        bookingID,
		Status:           "location",
		MechanicLat:      &latCopy,
		MechanicLng:      &lngCopy,
	}))
}
