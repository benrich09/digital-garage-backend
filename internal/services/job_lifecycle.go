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
	push *PushService
	log  zerolog.Logger
}

func NewJobLifecycleService(pool *pgxpool.Pool, hub *ws.Manager, log zerolog.Logger) *JobLifecycleService {
	return &JobLifecycleService{pool: pool, hub: hub, log: log}
}

func (s *JobLifecycleService) WithPush(p *PushService) *JobLifecycleService {
	s.push = p
	return s
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
		s.log.Debug().Str("car_owner", ownerID).Str("status", payload.Status).Str("booking", payload.BookingID).Msg("status_update -> car owner")
		if s.push != nil && payload.Status != "" {
			title := "Job update"
			body := "Status: " + payload.Status
			switch payload.Status {
			case "en_route":
				title, body = "Provider on the way", "Your mechanic/garage is en route."
			case "arrived":
				title, body = "Provider arrived", "They are at the location."
			case "in_progress":
				title, body = "Service started", "Work on your vehicle has started."
			case "completed", "awaiting_satisfaction":
				title, body = "Service finished", "Please confirm satisfaction and payment."
			case "billed", "awaiting_payment":
				title, body = "Bill ready", "Check the amount and confirm payment."
			case "paid":
				title, body = "Payment recorded", "Thank you. You can rate the service."
			}
			if uid, err := uuid.Parse(ownerID); err == nil {
				s.push.Notify(context.Background(), uid, title, body, map[string]string{
					"type":               "status_update",
					"status":             payload.Status,
					"booking_id":         payload.BookingID,
					"service_request_id": payload.ServiceRequestID,
				})
			}
		}
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
	isCustomer := actorRole == "car_owner" || actorRole == "Car Owner" || actorRole == "customer" || actorRole == "owner"
	isProvider := actorRole == "mechanic" || actorRole == "garage_owner" || actorRole == "Garage Owner" || actorRole == "Mechanic"
	if snap.RequestKind == "garage_booking" && !isCustomer {
		// Still allow if path forced customer; otherwise block provider pressing too early
		if isProvider {
			return snap, fmt.Errorf("wait for the customer to confirm they have arrived")
		}
	}
	// Mechanic request: only mechanic/provider confirms arrival.
	if snap.RequestKind != "garage_booking" && isCustomer {
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

// SetBill — provider sets price after service (prefer after satisfaction).
// Persists amount on bookings AND mirrors into service_transactions so the
// car-owner app can read it even when WS is down.
func (s *JobLifecycleService) SetBill(ctx context.Context, bookingID uuid.UUID, amount float64, currency string) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if amount <= 0 {
		return snap, fmt.Errorf("amount must be greater than zero")
	}
	if currency == "" {
		currency = "TZS"
	}
	phase := snap.Phase
	okToBill := snap.CustomerSatisfied ||
		phase == PhaseBilled ||
		phase == PhaseAwaitingPayment ||
		phase == PhaseCompleted ||
		phase == PhaseAwaitingSatisfaction ||
		phase == "completed" ||
		phase == "finished"
	if !okToBill {
		return snap, fmt.Errorf("wait until the customer confirms they are satisfied (current: %s)", phase)
	}

	// 1) bookings.bill_amount
	_, err = s.pool.Exec(ctx, `
		update bookings
		set bill_amount = $2,
		    currency = $3,
		    status = $4,
		    customer_satisfied = true,
		    updated_at = now()
		where id = $1
	`, bookingID, amount, currency, PhaseAwaitingPayment)
	if err != nil {
		s.log.Warn().Err(err).Msg("set bill with status failed — trying amount only")
		_, err2 := s.pool.Exec(ctx, `
			update bookings
			set bill_amount = $2, currency = $3, updated_at = now()
			where id = $1
		`, bookingID, amount, currency)
		if err2 != nil {
			s.log.Warn().Err(err2).Msg("bill_amount column may be missing")
		}
		_ = s.setPhase(ctx, bookingID, PhaseAwaitingPayment, "")
	}

	// 2) Mirror into service_transactions (car-owner polls this)
	if snap.ServiceRequestID != "" {
		if reqID, perr := uuid.Parse(snap.ServiceRequestID); perr == nil {
			_, terr := s.pool.Exec(ctx, `
				insert into service_transactions (request_id, amount, currency, status, created_at)
				values ($1, $2, $3, 'awaiting_confirmation', now())
			`, reqID, amount, currency)
			if terr != nil {
				// alternate column names
				_, _ = s.pool.Exec(ctx, `
					insert into service_transactions (service_request_id, amount, currency, status)
					values ($1, $2, $3, 'awaiting_confirmation')
				`, reqID, amount, currency)
				s.log.Warn().Err(terr).Msg("service_transactions insert failed")
			}
		}
	}

	// 3) Tag request description with [bill:amount]
	if snap.ServiceRequestID != "" {
		tag := fmt.Sprintf("[bill:%.0f %s]", amount, currency)
		_, _ = s.pool.Exec(ctx, `
			update service_requests
			set description = case
				when coalesce(description,'') = '' then $2
				when description like '%[bill:%' then description
				else description || E'
' || $2
			end,
			updated_at = now()
			where id = $1::uuid
		`, snap.ServiceRequestID, tag)
	}

	snap.Phase = PhaseAwaitingPayment
	snap.BillAmount = &amount
	snap.Currency = currency
	snap.CustomerSatisfied = true
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseAwaitingPayment,
	})
	s.log.Info().
		Str("booking_id", bookingID.String()).
		Float64("amount", amount).
		Str("currency", currency).
		Msg("bill set for customer")
	return snap, nil
}

// CustomerMarksPaid — customer says they paid; provider must verify.
func (s *JobLifecycleService) CustomerMarksPaid(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	// Allow from billed / awaiting_payment / already claimed
	ph := snap.Phase
	if ph != PhaseAwaitingPayment && ph != PhaseBilled && ph != "customer_claims_paid" && ph != "completed" {
		// still try to mark — soft so UI is not blocked after satisfaction path variants
		s.log.Warn().Str("phase", ph).Msg("mark-paid from unusual phase")
	}
	_, _ = s.pool.Exec(ctx, `
		update bookings
		set status = 'customer_claims_paid', updated_at = now()
		where id = $1
	`, bookingID)
	// Mirror on service_transactions so provider app refresh sees it
	if snap.ServiceRequestID != "" {
		if rid, e := uuid.Parse(snap.ServiceRequestID); e == nil {
			_, _ = s.pool.Exec(ctx, `
				update service_transactions
				set status = 'awaiting_provider_confirm'
				where request_id = $1
			`, rid)
			_, _ = s.pool.Exec(ctx, `
				update service_transactions
				set status = 'awaiting_provider_confirm'
				where service_request_id = $1
			`, rid)
		}
	}
	snap.Phase = "customer_claims_paid"
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           "customer_claims_paid",
	})
	s.log.Info().Str("booking_id", bookingID.String()).Msg("customer claims paid")
	return snap, nil
}

// ProviderConfirmPayment — garage/mechanic confirms money received.
func (s *JobLifecycleService) ProviderConfirmPayment(ctx context.Context, bookingID uuid.UUID, received bool) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	if !received {
		_, _ = s.pool.Exec(ctx, `update bookings set status = 'payment_rejected', updated_at = now() where id = $1`, bookingID)
		if snap.ServiceRequestID != "" {
			if rid, e := uuid.Parse(snap.ServiceRequestID); e == nil {
				_, _ = s.pool.Exec(ctx, `update service_transactions set status = 'rejected' where request_id = $1`, rid)
			}
		}
		s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
			ServiceRequestID: snap.ServiceRequestID,
			BookingID:        snap.BookingID,
			Status:           "payment_rejected",
		})
		return snap, fmt.Errorf("payment not received — ask the customer to pay")
	}
	_, err = s.pool.Exec(ctx, `
		update bookings
		set payment_confirmed = true, status = $2, updated_at = now()
		where id = $1
	`, bookingID, PhasePaid)
	if err != nil {
		_, _ = s.pool.Exec(ctx, `update bookings set status = $2, updated_at = now() where id = $1`, bookingID, PhasePaid)
	}
	if snap.ServiceRequestID != "" {
		if rid, e := uuid.Parse(snap.ServiceRequestID); e == nil {
			_, _ = s.pool.Exec(ctx, `update service_transactions set status = 'confirmed' where request_id = $1`, rid)
			_, _ = s.pool.Exec(ctx, `update service_transactions set status = 'confirmed' where service_request_id = $1`, rid)
		}
	}
	snap.Phase = PhasePaid
	snap.PaymentConfirmed = true
	s.syncRequest(ctx, snap.ServiceRequestID, PhasePaid)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhasePaid,
	})
	s.log.Info().Str("booking_id", bookingID.String()).Msg("provider confirmed payment")
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


// GetBillByRequest returns the latest booking bill for a service request,
// falling back to service_transactions amount.
func (s *JobLifecycleService) GetBillByRequest(ctx context.Context, requestID uuid.UUID) (JobSnapshot, error) {
	var bookingID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		select id from bookings
		where service_request_id = $1
		order by created_at desc nulls last
		limit 1
	`, requestID).Scan(&bookingID)
	if err == nil {
		snap, err2 := s.load(ctx, bookingID)
		if err2 == nil && snap.BillAmount != nil {
			return snap, nil
		}
		if err2 == nil {
			// try transactions
			var amount float64
			var currency string
			terr := s.pool.QueryRow(ctx, `
				select amount::float8, coalesce(currency, 'TZS')
				from service_transactions
				where request_id = $1
				order by created_at desc
				limit 1
			`, requestID).Scan(&amount, &currency)
			if terr != nil {
				terr = s.pool.QueryRow(ctx, `
					select amount::float8, coalesce(currency, 'TZS')
					from service_transactions
					where service_request_id = $1
					order by created_at desc
					limit 1
				`, requestID).Scan(&amount, &currency)
			}
			if terr == nil {
				snap.BillAmount = &amount
				snap.Currency = currency
				snap.Phase = PhaseAwaitingPayment
			}
			return snap, nil
		}
	}
	// transactions only
	var amount float64
	var currency string
	terr := s.pool.QueryRow(ctx, `
		select amount::float8, coalesce(currency, 'TZS')
		from service_transactions
		where request_id = $1
		order by created_at desc
		limit 1
	`, requestID).Scan(&amount, &currency)
	if terr != nil {
		return JobSnapshot{}, fmt.Errorf("no bill yet")
	}
	return JobSnapshot{
		ServiceRequestID: requestID.String(),
		Phase:            PhaseAwaitingPayment,
		BillAmount:       &amount,
		Currency:         currency,
	}, nil
}
