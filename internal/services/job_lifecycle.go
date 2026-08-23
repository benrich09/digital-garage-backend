package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/ws"
)

// Job phase strings stored on bookings.status (text-friendly).
// Sequence:
//
//	scheduled → awaiting_customer (garage) | en_route (mechanic)
//	→ arrived → in_progress → completed → awaiting_satisfaction
//	→ billed → awaiting_payment → paid → closed
const (
	PhaseScheduled            = "scheduled"
	PhaseEnRoute              = "en_route"
	PhaseAwaitingCustomer     = "awaiting_customer"
	PhaseArrived              = "arrived"
	PhaseInProgress           = "in_progress"
	PhaseCompleted            = "completed"
	PhaseAwaitingSatisfaction = "awaiting_satisfaction"
	PhaseBilled               = "billed"
	PhaseAwaitingPayment      = "awaiting_payment"
	PhasePaid                 = "paid"
	PhaseClosed               = "closed"
	PhaseCancelled            = "cancelled"
)

var jobTransitions = map[string][]string{
	PhaseScheduled:            {PhaseEnRoute, PhaseAwaitingCustomer, PhaseArrived, PhaseCancelled},
	PhaseEnRoute:              {PhaseArrived, PhaseCancelled},
	PhaseAwaitingCustomer:     {PhaseArrived, PhaseCancelled},
	PhaseArrived:              {PhaseInProgress, PhaseCancelled},
	PhaseInProgress:           {PhaseCompleted, PhaseCancelled},
	PhaseCompleted:            {PhaseAwaitingSatisfaction},
	PhaseAwaitingSatisfaction: {PhaseBilled},
	PhaseBilled:               {PhaseAwaitingPayment},
	PhaseAwaitingPayment:      {PhasePaid},
	PhasePaid:                 {PhaseClosed},
}

// JobLifecycleService drives the garage-booking and mechanic-request sequences.
type JobLifecycleService struct {
	pool *pgxpool.Pool
	hub  *ws.Manager
	log  zerolog.Logger
}

func NewJobLifecycleService(pool *pgxpool.Pool, hub *ws.Manager, log zerolog.Logger) *JobLifecycleService {
	return &JobLifecycleService{pool: pool, hub: hub, log: log}
}

type JobSnapshot struct {
	BookingID        string     `json:"booking_id"`
	ServiceRequestID string     `json:"service_request_id"`
	Phase            string     `json:"phase"`
	RequestKind      string     `json:"request_kind"`
	CarOwnerID       string     `json:"car_owner_id"`
	ProviderID       string     `json:"provider_id,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	BillAmount       *float64   `json:"bill_amount,omitempty"`
	Currency         string     `json:"currency,omitempty"`
	PaymentConfirmed bool       `json:"payment_confirmed"`
	CustomerSatisfied bool      `json:"customer_satisfied"`
}

func (s *JobLifecycleService) load(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	var snap JobSnapshot
	var started, completed *time.Time
	var amount *float64
	var currency string
	var paid, satisfied bool
	err := s.pool.QueryRow(ctx, `
		select
			b.id::text,
			b.service_request_id::text,
			coalesce(b.status, 'scheduled'),
			coalesce(sr.request_kind, case when sr.description like '%[kind:garage_booking]%' then 'garage_booking' else 'mechanic_request' end),
			sr.car_owner_id::text,
			coalesce(m.profile_id::text, g.owner_id::text, ''),
			b.started_at,
			b.completed_at,
			b.bill_amount,
			coalesce(b.currency, 'TZS'),
			coalesce(b.payment_confirmed, false),
			coalesce(b.customer_satisfied, false)
		from bookings b
		join service_requests sr on sr.id = b.service_request_id
		left join mechanics m on m.id = b.mechanic_id
		left join garages g on g.id = b.garage_id
		where b.id = $1
	`, bookingID).Scan(
		&snap.BookingID, &snap.ServiceRequestID, &snap.Phase, &snap.RequestKind,
		&snap.CarOwnerID, &snap.ProviderID, &started, &completed, &amount, &currency, &paid, &satisfied,
	)
	if err != nil {
		// Fallback without optional columns (bill_amount etc.)
		err2 := s.pool.QueryRow(ctx, `
			select
				b.id::text,
				b.service_request_id::text,
				coalesce(b.status, 'scheduled'),
				coalesce(sr.description, ''),
				sr.car_owner_id::text
			from bookings b
			join service_requests sr on sr.id = b.service_request_id
			where b.id = $1
		`, bookingID).Scan(&snap.BookingID, &snap.ServiceRequestID, &snap.Phase, &snap.RequestKind, &snap.CarOwnerID)
		if err2 != nil {
			return snap, fmt.Errorf("load booking: %w", err)
		}
		if contains(snap.RequestKind, "garage_booking") {
			snap.RequestKind = "garage_booking"
		} else {
			snap.RequestKind = "mechanic_request"
		}
	} else {
		snap.StartedAt = started
		snap.CompletedAt = completed
		snap.BillAmount = amount
		snap.Currency = currency
		snap.PaymentConfirmed = paid
		snap.CustomerSatisfied = satisfied
	}
	return snap, nil
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (s *JobLifecycleService) canTransition(from, to string) bool {
	if from == to {
		return true
	}
	for _, a := range jobTransitions[from] {
		if a == to {
			return true
		}
	}
	// Legacy: scheduled/in_progress/completed still accepted
	legacy := map[string][]string{
		"scheduled":   {PhaseEnRoute, PhaseAwaitingCustomer, PhaseArrived, PhaseInProgress, PhaseCancelled},
		"in_progress": {PhaseCompleted, PhaseCancelled},
		"completed":   {PhaseAwaitingSatisfaction, PhaseBilled, PhaseAwaitingPayment, PhasePaid},
	}
	for _, a := range legacy[from] {
		if a == to {
			return true
		}
	}
	return false
}

func (s *JobLifecycleService) setPhase(ctx context.Context, bookingID uuid.UUID, phase string, extraSQL string, args ...any) error {
	q := `update bookings set status = $2, updated_at = now()`
	if extraSQL != "" {
		q += ", " + extraSQL
	}
	q += ` where id = $1`
	all := append([]any{bookingID, phase}, args...)
	_, err := s.pool.Exec(ctx, q, all...)
	return err
}

func (s *JobLifecycleService) syncRequest(ctx context.Context, requestID, phase string) {
	reqStatus := map[string]string{
		PhaseScheduled:            "accepted",
		PhaseEnRoute:              "accepted",
		PhaseAwaitingCustomer:     "accepted",
		PhaseArrived:              "accepted",
		PhaseInProgress:           "in_progress",
		PhaseCompleted:            "completed",
		PhaseAwaitingSatisfaction: "completed",
		PhaseBilled:               "completed",
		PhaseAwaitingPayment:      "completed",
		PhasePaid:                 "paid",
		PhaseClosed:               "closed",
		PhaseCancelled:            "cancelled",
	}[phase]
	if reqStatus == "" {
		return
	}
	_, _ = s.pool.Exec(ctx, `update service_requests set status = $2, updated_at = now() where id = $1::uuid`, requestID, reqStatus)
}

func (s *JobLifecycleService) notify(ownerID, providerID string, payload ws.StatusUpdatePayload) {
	if s.hub == nil {
		return
	}
	ev := ws.NewEvent(ws.EventStatusUpdate, payload)
	if ownerID != "" {
		s.hub.SendToUser(ownerID, ev)
	}
	if providerID != "" {
		s.hub.SendToUser(providerID, ev)
	}
}

// ProviderAcceptAfterMatch — after approve: garage waits for customer; mechanic goes en_route.
func (s *JobLifecycleService) OnProviderAccepted(ctx context.Context, bookingID uuid.UUID, kind string) error {
	phase := PhaseEnRoute
	if kind == "garage_booking" {
		phase = PhaseAwaitingCustomer
	}
	if err := s.setPhase(ctx, bookingID, phase, ""); err != nil {
		// Fallback: keep scheduled if enum rejects new values
		_ = s.setPhase(ctx, bookingID, PhaseScheduled, "")
		phase = PhaseScheduled
	}
	snap, _ := s.load(ctx, bookingID)
	s.syncRequest(ctx, snap.ServiceRequestID, phase)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           phase,
	})
	return nil
}

// ConfirmArrival — mechanic confirms arrival at customer, OR customer confirms arrival at garage.
func (s *JobLifecycleService) ConfirmArrival(ctx context.Context, bookingID uuid.UUID, actorRole string) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	// Garage booking: only customer confirms arrival.
	if snap.RequestKind == "garage_booking" && actorRole != "car_owner" {
		return snap, fmt.Errorf("wait for the customer to confirm they have arrived")
	}
	// Mechanic request: only mechanic/provider confirms arrival.
	if snap.RequestKind != "garage_booking" && actorRole == "car_owner" {
		return snap, fmt.Errorf("wait for the mechanic to confirm arrival")
	}
	if !s.canTransition(snap.Phase, PhaseArrived) && snap.Phase != PhaseScheduled && snap.Phase != PhaseEnRoute && snap.Phase != PhaseAwaitingCustomer {
		return snap, fmt.Errorf("cannot confirm arrival from phase %s", snap.Phase)
	}
	if err := s.setPhase(ctx, bookingID, PhaseArrived, ""); err != nil {
		return snap, err
	}
	snap.Phase = PhaseArrived
	s.syncRequest(ctx, snap.ServiceRequestID, PhaseArrived)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseArrived,
	})
	return snap, nil
}

// StartService — only after arrival.
func (s *JobLifecycleService) StartService(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if snap.Phase != PhaseArrived {
		return snap, fmt.Errorf("confirm arrival before starting service (current: %s)", snap.Phase)
	}
	now := time.Now().UTC()
	if err := s.setPhase(ctx, bookingID, PhaseInProgress, `started_at = $3`, now); err != nil {
		// Without started_at column
		if err2 := s.setPhase(ctx, bookingID, PhaseInProgress, ""); err2 != nil {
			return snap, err2
		}
	}
	snap.Phase = PhaseInProgress
	snap.StartedAt = &now
	s.syncRequest(ctx, snap.ServiceRequestID, PhaseInProgress)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseInProgress,
		StartedAt:        &now,
	})
	return snap, nil
}

// FinishService — provider finishes; waits for customer satisfaction.
func (s *JobLifecycleService) FinishService(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if snap.Phase != PhaseInProgress {
		return snap, fmt.Errorf("service is not in progress (current: %s)", snap.Phase)
	}
	now := time.Now().UTC()
	if err := s.setPhase(ctx, bookingID, PhaseAwaitingSatisfaction, `completed_at = $3`, now); err != nil {
		_ = s.setPhase(ctx, bookingID, PhaseCompleted, "")
		snap.Phase = PhaseCompleted
	} else {
		snap.Phase = PhaseAwaitingSatisfaction
	}
	snap.CompletedAt = &now
	s.syncRequest(ctx, snap.ServiceRequestID, PhaseCompleted)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           snap.Phase,
	})
	if s.hub != nil {
		s.hub.SendToUser(snap.CarOwnerID, ws.NewEvent(ws.EventJobCompleted, ws.JobCompletedPayload{
			ServiceRequestID: snap.ServiceRequestID,
			BookingID:        snap.BookingID,
			StartedAt:        snap.StartedAt,
			CompletedAt:      &now,
		}))
	}
	return snap, nil
}

// ConfirmSatisfaction — customer confirms service was OK; then provider may set price.
func (s *JobLifecycleService) ConfirmSatisfaction(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	// Allow from finished states; also tolerate status enum lag.
	okPhase := map[string]bool{
		PhaseAwaitingSatisfaction: true,
		PhaseCompleted:            true, // "completed"
		"finished":                true,
		"done":                    true,
	}
	if !okPhase[snap.Phase] && snap.Phase != PhaseInProgress {
		// Still allow if service already completed_at is set
		if snap.CompletedAt == nil {
			return snap, fmt.Errorf("cannot confirm satisfaction from phase %s", snap.Phase)
		}
	}
	_, _ = s.pool.Exec(ctx, `update bookings set customer_satisfied = true, updated_at = now() where id = $1`, bookingID)
	// Move to billed (ready for provider to enter amount) — not payment yet
	if err := s.setPhase(ctx, bookingID, PhaseBilled, ""); err != nil {
		_ = s.setPhase(ctx, bookingID, PhaseCompleted, "")
		// last resort: keep status, flag satisfied only
	}
	snap.Phase = PhaseBilled
	snap.CustomerSatisfied = true
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseBilled,
	})
	return snap, nil
}

// SetBill — provider confirms price AFTER customer satisfaction only.
func (s *JobLifecycleService) SetBill(ctx context.Context, bookingID uuid.UUID, amount float64, currency string) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if !snap.CustomerSatisfied && snap.Phase != PhaseBilled && snap.Phase != PhaseAwaitingPayment {
		return snap, fmt.Errorf("wait until the customer confirms they are satisfied")
	}
	if amount <= 0 {
		return snap, fmt.Errorf("amount must be greater than zero")
	}
	if currency == "" {
		currency = "TZS"
	}
	_, err = s.pool.Exec(ctx, `
		update bookings
		set bill_amount = $2, currency = $3, status = $4, updated_at = now()
		where id = $1
	`, bookingID, amount, currency, PhaseAwaitingPayment)
	if err != nil {
		// columns may not exist — still advance phase
		_ = s.setPhase(ctx, bookingID, PhaseAwaitingPayment, "")
	}
	snap.Phase = PhaseAwaitingPayment
	snap.BillAmount = &amount
	snap.Currency = currency
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseAwaitingPayment,
	})
	return snap, nil
}

// CustomerMarksPaid — customer says they paid.
func (s *JobLifecycleService) CustomerMarksPaid(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if snap.Phase != PhaseAwaitingPayment && snap.Phase != PhaseBilled {
		return snap, fmt.Errorf("no bill waiting for payment (current: %s)", snap.Phase)
	}
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           "customer_claims_paid",
	})
	return snap, nil
}

// ProviderConfirmPayment — garage/mechanic confirms money received.
func (s *JobLifecycleService) ProviderConfirmPayment(ctx context.Context, bookingID uuid.UUID, received bool) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if !received {
		s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
			ServiceRequestID: snap.ServiceRequestID,
			BookingID:        snap.BookingID,
			Status:           "payment_rejected",
		})
		return snap, fmt.Errorf("payment not received — ask the customer to pay")
	}
	_, _ = s.pool.Exec(ctx, `update bookings set payment_confirmed = true, status = $2, updated_at = now() where id = $1`, bookingID, PhasePaid)
	snap.Phase = PhasePaid
	snap.PaymentConfirmed = true
	s.syncRequest(ctx, snap.ServiceRequestID, PhasePaid)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhasePaid,
	})
	return snap, nil
}

// GetSnapshot public
func (s *JobLifecycleService) GetSnapshot(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	return s.load(ctx, bookingID)
}


// ConfirmSatisfactionByRequest finds the latest booking for a service request.
func (s *JobLifecycleService) ConfirmSatisfactionByRequest(ctx context.Context, requestID uuid.UUID) (JobSnapshot, error) {
	var bookingID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		select id from bookings
		where service_request_id = $1
		order by created_at desc nulls last
		limit 1
	`, requestID).Scan(&bookingID)
	if err != nil {
		return JobSnapshot{}, fmt.Errorf("no booking for this request yet")
	}
	return s.ConfirmSatisfaction(ctx, bookingID)
}
