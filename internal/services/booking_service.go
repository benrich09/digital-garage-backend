package services

import (
	"context"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	bookings   repository.BookingRepository
	requests   repository.ServiceRequestRepository
	commission *CommissionService
	pool       *pgxpool.Pool
	hub        *ws.Manager
	log        zerolog.Logger
}

func NewBookingService(bookings repository.BookingRepository, requests repository.ServiceRequestRepository, hub *ws.Manager, log zerolog.Logger) *BookingService {
	return &BookingService{bookings: bookings, requests: requests, hub: hub, log: log}
}

func (s *BookingService) WithCommission(c *CommissionService) *BookingService {
	s.commission = c
	return s
}

func (s *BookingService) WithPool(pool *pgxpool.Pool) *BookingService {
	s.pool = pool
	return s
}

func (s *BookingService) GetByRequest(ctx context.Context, requestID uuid.UUID) (models.Booking, error) {
	return s.bookings.GetByServiceRequest(ctx, requestID)
}

// SetStatus transitions a booking (e.g. garage/mechanic marks the job
// started or completed) and notifies the car owner in real time.
// On completed, auto-creates a service_transaction from the accepted
// offer price so the customer is billed without the provider typing an amount.
func (s *BookingService) SetStatus(ctx context.Context, bookingID uuid.UUID, newStatus string) error {
	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return fmt.Errorf("load booking: %w", err)
	}

	// Idempotent: already at target status
	if booking.Status == newStatus {
		if newStatus == "completed" {
			s.autoBill(ctx, booking)
		}
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
	// owner polling GET /service-requests/mine sees consistent state.
	requestStatus := map[string]string{
		"in_progress": "in_progress",
		"completed":   "completed",
		"cancelled":   "cancelled",
	}
	if rs, has := requestStatus[newStatus]; has {
		_ = s.requests.UpdateStatus(ctx, booking.ServiceRequestID, rs)
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err == nil {
		s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
			ServiceRequestID: booking.ServiceRequestID.String(),
			BookingID:        booking.ID.String(),
			Status:           newStatus,
		}))
		if newStatus == "completed" {
			s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventJobCompleted, ws.JobCompletedPayload{
				ServiceRequestID: booking.ServiceRequestID.String(),
				BookingID:        booking.ID.String(),
			}))
			s.autoBill(ctx, booking)
		}
	}

	return nil
}

// autoBill creates a transaction from the accepted offer price (or provider_services).
func (s *BookingService) autoBill(ctx context.Context, booking models.Booking) {
	if s.commission == nil || s.pool == nil {
		return
	}
	// Skip if a txn already exists for this booking/request.
	var existing int
	_ = s.pool.QueryRow(ctx, `
		select count(*) from service_transactions
		where (booking_id = $1 or request_id = $2)
		  and status in ('awaiting_confirmation', 'confirmed')
	`, booking.ID, booking.ServiceRequestID).Scan(&existing)
	if existing > 0 {
		return
	}

	var (
		priceStr   string
		currency   string
		providerID uuid.UUID
		garageID   *uuid.UUID
		serviceName string
		carOwnerID uuid.UUID
	)
	err := s.pool.QueryRow(ctx, `
		select
			coalesce(nullif(o.price::text, ''), '0'),
			coalesce(o.currency, 'TZS'),
			coalesce(m.profile_id, g.owner_id),
			o.garage_id,
			coalesce(sr.description, sc.name, 'Service job'),
			sr.car_owner_id
		from bookings b
		join offers o on o.id = b.offer_id
		join service_requests sr on sr.id = b.service_request_id
		left join service_categories sc on sc.id = sr.category_id
		left join mechanics m on m.id = b.mechanic_id
		left join garages g on g.id = b.garage_id
		where b.id = $1
	`, booking.ID).Scan(&priceStr, &currency, &providerID, &garageID, &serviceName, &carOwnerID)
	if err != nil {
		s.log.Warn().Err(err).Str("booking_id", booking.ID.String()).Msg("autoBill: could not load offer price")
		return
	}
	amount, _ := strconv.ParseFloat(priceStr, 64)
	if amount <= 0 {
		// fallback provider_services (price registered by mechanic/garage)
		_ = s.pool.QueryRow(ctx, `
			select coalesce(ps.price::text, '0')
			from provider_services ps
			where ps.provider_id = $1 and ps.is_active = true
			order by ps.is_roadside desc, ps.created_at desc limit 1
		`, providerID).Scan(&priceStr)
		amount, _ = strconv.ParseFloat(priceStr, 64)
	}
	// Roadside / mechanic: add distance fee (TZS 2,000 per km, min 0)
	var distM float64
	_ = s.pool.QueryRow(ctx, `
		select coalesce(
			ST_Distance(
				sr.pickup_location,
				coalesce(m.current_location, g.location)
			), 0)
		from bookings b
		join service_requests sr on sr.id = b.service_request_id
		left join mechanics m on m.id = b.mechanic_id
		left join garages g on g.id = b.garage_id
		where b.id = $1
	`, booking.ID).Scan(&distM)
	if distM > 0 && booking.MechanicID != nil {
		km := distM / 1000.0
		travel := km * 2000.0 // TZS per km
		amount += travel
		serviceName = fmt.Sprintf("%s | service base + travel %.1f km (TZS %.0f)", serviceName, km, travel)
		s.log.Info().Float64("km", km).Float64("travel", travel).Msg("autoBill: added mechanic travel fee")
	}

	if amount <= 0 {
		s.log.Warn().Str("booking_id", booking.ID.String()).Msg("autoBill: no positive price — set prices under My services")
		return
	}

	bid := booking.ID
	rid := booking.ServiceRequestID
	in := models.RecordServiceInput{
		CarOwnerID:  carOwnerID,
		BookingID:   &bid,
		RequestID:   &rid,
		GarageID:    garageID,
		ServiceName: serviceName,
		Amount:      amount,
		Currency:    currency,
		PaidMethod:  "cash",
	}
	if _, err := s.commission.RecordService(ctx, providerID, in); err != nil {
		s.log.Warn().Err(err).Msg("autoBill: RecordService failed")
		return
	}
	s.log.Info().
		Str("booking_id", booking.ID.String()).
		Float64("amount", amount).
		Msg("autoBill: customer billed from agreed service price")
}
