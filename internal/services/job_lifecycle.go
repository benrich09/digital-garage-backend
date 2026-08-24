package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
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
	pool       *pgxpool.Pool
	hub        *ws.Manager
	push       *PushService
	commission *CommissionService
	log        zerolog.Logger
}

func NewJobLifecycleService(pool *pgxpool.Pool, hub *ws.Manager, log zerolog.Logger) *JobLifecycleService {
	return &JobLifecycleService{pool: pool, hub: hub, log: log}
}

func (s *JobLifecycleService) WithPush(p *PushService) *JobLifecycleService {
	s.push = p
	return s
}

func (s *JobLifecycleService) WithCommission(c *CommissionService) *JobLifecycleService {
	s.commission = c
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
	CustomerSatisfied  bool      `json:"customer_satisfied"`
	CommissionExpected  *float64   `json:"commission_expected,omitempty"`
	TransactionID       string     `json:"transaction_id,omitempty"`
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

// SetBill — provider confirms/sets the service price after work is done.
// Pipeline:
//  1) Persist amount on bookings (admin + apps read bill_amount)
//  2) Upsert service_transactions so the admin dashboard shows the job
//  3) Notify car owner (WS + optional push) that a bill is ready
//  4) Surface expected platform commission (CommissionRate) in the response
// Commission ledger debit is booked when payment is confirmed
// (ProviderConfirmPayment / car-owner Confirm), not at price-set time.
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
		phase == "finished" ||
		phase == PhaseInProgress ||
		phase == "in_progress"
	if !okToBill {
		return snap, fmt.Errorf("wait until the service is finished (current: %s)", phase)
	}

	// --- 1) bookings.bill_amount (source of truth for price) ------------
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

	// Resolve parties for dashboard row
	providerID := snap.ProviderID
	if providerID == "" {
		var pid string
		_ = s.pool.QueryRow(ctx, `
			select coalesce(
				(select g.owner_id::text from garages g join bookings b on b.garage_id = g.id where b.id = $1),
				(select m.profile_id::text from mechanics m join bookings b on b.mechanic_id = m.id where b.id = $1)
			)
		`, bookingID).Scan(&pid)
		providerID = pid
		snap.ProviderID = pid
	}
	carOwnerID := snap.CarOwnerID
	var garageID *uuid.UUID
	var gid uuid.UUID
	if e := s.pool.QueryRow(ctx, `select garage_id from bookings where id = $1`, bookingID).Scan(&gid); e == nil && gid != uuid.Nil {
		garageID = &gid
	}
	serviceName := "Vehicle service"
	if snap.ServiceRequestID != "" {
		var cat string
		_ = s.pool.QueryRow(ctx, `
			select coalesce(sc.name, 'Vehicle service')
			from service_requests sr
			left join service_categories sc on sc.id = sr.category_id
			where sr.id = $1::uuid
		`, snap.ServiceRequestID).Scan(&cat)
		if cat != "" {
			serviceName = cat
		}
	}

	// --- 2) service_transactions for admin dashboard + car-owner confirm --
	txnID := ""
	if s.commission != nil && providerID != "" && carOwnerID != "" {
		pUUID, perr := uuid.Parse(providerID)
		cUUID, cerr := uuid.Parse(carOwnerID)
		if perr == nil && cerr == nil {
			bid := bookingID
			var rid *uuid.UUID
			if snap.ServiceRequestID != "" {
				if parsed, e := uuid.Parse(snap.ServiceRequestID); e == nil {
					rid = &parsed
				}
			}
			in := models.RecordServiceInput{
				CarOwnerID:  cUUID,
				BookingID:   &bid,
				RequestID:   rid,
				GarageID:    garageID,
				ServiceName: serviceName,
				Amount:      amount,
				Currency:    currency,
				PaidMethod:  "cash",
			}
			txn, rerr := s.commission.RecordService(ctx, pUUID, in)
			if rerr != nil {
				s.log.Warn().Err(rerr).Msg("RecordService after SetBill failed — falling back to direct insert")
			} else {
				txnID = txn.ID.String()
			}
		}
	}
	// Direct upsert fallback (idempotent on booking_id when possible)
	if txnID == "" && carOwnerID != "" && providerID != "" {
		var existing string
		_ = s.pool.QueryRow(ctx, `
			select id::text from service_transactions
			where booking_id = $1
			order by created_at desc nulls last limit 1
		`, bookingID).Scan(&existing)
		if existing != "" {
			_, _ = s.pool.Exec(ctx, `
				update service_transactions
				set amount = $2, currency = $3, status = 'awaiting_confirmation', service_name = $4
				where id = $1::uuid
			`, existing, amount, currency, serviceName)
			txnID = existing
		} else {
			nid := uuid.New()
			_, ierr := s.pool.Exec(ctx, `
				insert into service_transactions (
					id, booking_id, request_id, car_owner_id, provider_id, garage_id,
					service_name, amount, currency, paid_method, status, created_at
				) values (
					$1, $2, nullif($3,'')::uuid, $4::uuid, $5::uuid, $6,
					$7, $8, $9, 'cash', 'awaiting_confirmation', now()
				)
			`, nid, bookingID, snap.ServiceRequestID, carOwnerID, providerID, garageID,
				serviceName, amount, currency)
			if ierr != nil {
				// Minimal columns
				_, ierr2 := s.pool.Exec(ctx, `
					insert into service_transactions (request_id, amount, currency, status, created_at)
					values (nullif($1,'')::uuid, $2, $3, 'awaiting_confirmation', now())
				`, snap.ServiceRequestID, amount, currency)
				if ierr2 != nil {
					s.log.Warn().Err(ierr).Err(ierr2).Msg("service_transactions insert failed")
				} else {
					txnID = nid.String()
				}
			} else {
				txnID = nid.String()
			}
		}
	}

	// Tag request description for debugging / offline clients
	if snap.ServiceRequestID != "" {
		tag := fmt.Sprintf("[bill:%.0f %s]", amount, currency)
		_, _ = s.pool.Exec(ctx, `
			update service_requests
			set description = case
				when description is null or description = '' then $2
				when description like '%%[bill:%%' then regexp_replace(description, '\[bill:[^\]]+\]', $2)
				else description || E'\n' || $2
			end,
			status = 'awaiting_payment',
			updated_at = now()
			where id = $1::uuid
		`, snap.ServiceRequestID, tag)
	}

	// --- 3) Notify car owner: bill ready --------------------------------
	expected := CommissionOn(amount)
	snap.BillAmount = &amount
	snap.Currency = currency
	snap.Phase = PhaseAwaitingPayment
	snap.CommissionExpected = &expected
	snap.TransactionID = txnID

	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseAwaitingPayment,
	})
	if s.hub != nil && snap.CarOwnerID != "" {
		s.hub.SendToUser(snap.CarOwnerID, ws.NewEvent(ws.EventConfirmationRequested, ws.ConfirmationRequestedPayload{
			TransactionID: txnID,
			ServiceName:   serviceName,
			Amount:        amount,
			Currency:      currency,
		}))
	}
	if s.push != nil && snap.CarOwnerID != "" {
		if uid, e := uuid.Parse(snap.CarOwnerID); e == nil {
			s.push.Notify(ctx, uid, "Bill ready",
				fmt.Sprintf("Your service costs %.0f %s. Confirm payment in the app.", amount, currency),
				map[string]string{
					"type":               "bill_ready",
					"booking_id":         snap.BookingID,
					"service_request_id": snap.ServiceRequestID,
					"amount":             fmt.Sprintf("%.2f", amount),
					"commission":         fmt.Sprintf("%.2f", expected),
				})
		}
	}

	s.log.Info().
		Str("booking_id", snap.BookingID).
		Float64("amount", amount).
		Float64("commission_expected", expected).
		Str("transaction_id", txnID).
		Str("provider_id", providerID).
		Msg("provider price set — dashboard transaction ready, commission pending confirmation")

	return snap, nil
}

// CustomerMarksPaid — customer says they paid; provider must verify.
func (s *JobLifecycleService) CustomerMarksPaid(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	ph := snap.Phase
	if ph != PhaseAwaitingPayment && ph != PhaseBilled && ph != "customer_claims_paid" && ph != "completed" {
		s.log.Warn().Str("phase", ph).Msg("mark-paid from unusual phase")
	}
	_, _ = s.pool.Exec(ctx, `
		update bookings
		set status = 'customer_claims_paid', updated_at = now()
		where id = $1
	`, bookingID)
	// Keep service_transactions as awaiting_confirmation (valid enum).
	// Link booking_id so provider confirm can find the row.
	_, _ = s.pool.Exec(ctx, `
		update service_transactions
		set booking_id = coalesce(booking_id, $1)
		where booking_id = $1
		   or ($2 <> '' and (request_id::text = $2 or service_request_id::text = $2))
	`, bookingID, snap.ServiceRequestID)
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
		_, _ = s.pool.Exec(ctx, `
			update service_transactions set status = 'disputed'
			where booking_id = $1
			   or ($2 <> '' and (request_id::text = $2 or service_request_id::text = $2))
		`, bookingID, snap.ServiceRequestID)
		s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
			ServiceRequestID: snap.ServiceRequestID,
			BookingID:        snap.BookingID,
			Status:           "payment_rejected",
		})
		return snap, fmt.Errorf("payment not received")
	}

	// Resolve parties + amount
	providerID := snap.ProviderID
	if providerID == "" {
		var pid string
		_ = s.pool.QueryRow(ctx, `
			select coalesce(
				(select g.owner_id::text from garages g join bookings b on b.garage_id = g.id where b.id = $1),
				(select m.profile_id::text from mechanics m join bookings b on b.mechanic_id = m.id where b.id = $1)
			)
		`, bookingID).Scan(&pid)
		providerID = pid
		snap.ProviderID = pid
	}
	amount := 0.0
	if snap.BillAmount != nil {
		amount = *snap.BillAmount
	}
	if amount <= 0 {
		_ = s.pool.QueryRow(ctx, `select coalesce(bill_amount,0) from bookings where id = $1`, bookingID).Scan(&amount)
	}
	if amount <= 0 {
		_ = s.pool.QueryRow(ctx, `
			select coalesce(amount,0) from service_transactions
			where booking_id = $1
			   or ($2 <> '' and (request_id::text = $2 or service_request_id::text = $2))
			order by created_at desc nulls last limit 1
		`, bookingID, snap.ServiceRequestID).Scan(&amount)
	}

	// Mark booking paid (source of truth for job phase)
	_, err = s.pool.Exec(ctx, `
		update bookings
		set status = $2, payment_confirmed = true, updated_at = now()
		where id = $1
	`, bookingID, PhasePaid)
	if err != nil {
		s.log.Warn().Err(err).Msg("booking paid update failed")
		_ = s.setPhase(ctx, bookingID, PhasePaid, "")
	}

	// Ensure a service_transactions row exists, then set status=confirmed
	// (trigger books commission_debit; constraint needs confirmed_at + confirmed_by)
	txnID := ""
	_ = s.pool.QueryRow(ctx, `
		select id::text from service_transactions
		where booking_id = $1
		   or ($2 <> '' and (request_id::text = $2 or service_request_id::text = $2))
		order by created_at desc nulls last limit 1
	`, bookingID, snap.ServiceRequestID).Scan(&txnID)

	confirmer := snap.CarOwnerID
	if confirmer == "" {
		confirmer = providerID
	}
	serviceName := "Vehicle service"
	if snap.ServiceRequestID != "" {
		var n string
		_ = s.pool.QueryRow(ctx, `
			select coalesce(sc.name, 'Vehicle service')
			from service_requests sr
			left join service_categories sc on sc.id = sr.category_id
			where sr.id = $1::uuid
		`, snap.ServiceRequestID).Scan(&n)
		if n != "" {
			serviceName = n
		}
	}

	if txnID == "" && amount > 0 && providerID != "" && snap.CarOwnerID != "" {
		nid := uuid.New()
		_, ierr := s.pool.Exec(ctx, `
			insert into service_transactions (
				id, booking_id, request_id, car_owner_id, provider_id,
				service_name, amount, currency, paid_method, status,
				confirmed_at, confirmed_by, created_at
			) values (
				$1, $2, nullif($3,'')::uuid, $4::uuid, $5::uuid,
				$6, $7, 'TZS', 'cash', 'confirmed',
				now(), $4::uuid, now()
			)
		`, nid, bookingID, snap.ServiceRequestID, snap.CarOwnerID, providerID, serviceName, amount)
		if ierr != nil {
			s.log.Warn().Err(ierr).Msg("create confirmed transaction failed")
		} else {
			txnID = nid.String()
		}
	}

	if txnID != "" {
		// Force confirmed — satisfies CHECK (confirmed_at, confirmed_by)
		tag, uerr := s.pool.Exec(ctx, `
			update service_transactions
			set status = 'confirmed',
			    confirmed_at = coalesce(confirmed_at, now()),
			    confirmed_by = coalesce(confirmed_by, nullif($2,'')::uuid, car_owner_id),
			    amount = case when $3 > 0 then $3 else amount end,
			    booking_id = coalesce(booking_id, $4),
			    provider_id = coalesce(provider_id, nullif($5,'')::uuid)
			where id = $1::uuid
		`, txnID, confirmer, amount, bookingID, providerID)
		if uerr != nil {
			s.log.Warn().Err(uerr).Msg("confirm transaction update failed")
			// last resort without enum constraints
			_, _ = s.pool.Exec(ctx, `
				update service_transactions
				set status = 'confirmed', confirmed_at = now()
				where id = $1::uuid
			`, txnID)
		} else {
			s.log.Info().Str("txn", txnID).Int64("rows", tag.RowsAffected()).Msg("service_transaction confirmed")
		}
		snap.TransactionID = txnID

		// Prefer CommissionService.Confirm when car owner id known
		if s.commission != nil && snap.CarOwnerID != "" {
			if tid, e := uuid.Parse(txnID); e == nil {
				if cid, e2 := uuid.Parse(snap.CarOwnerID); e2 == nil {
					if _, e3 := s.commission.Confirm(ctx, cid, tid); e3 != nil {
						s.log.Debug().Err(e3).Msg("commission.Confirm optional path")
					}
				}
			}
		}
	}

	// Always book ledger commission_debit (idempotent) — this is what
	// provider_balances reads. Wrong entry_type 'debit' was previously ignored.
	s.bookCommissionOnPaid(ctx, snap)

	if amount > 0 {
		snap.BillAmount = &amount
		exp := CommissionOn(amount)
		snap.CommissionExpected = &exp
	}
	snap.Phase = PhasePaid
	snap.PaymentConfirmed = true

	// Request status
	if snap.ServiceRequestID != "" {
		_, _ = s.pool.Exec(ctx, `
			update service_requests set status = 'paid', updated_at = now() where id = $1::uuid
		`, snap.ServiceRequestID)
	}

	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhasePaid,
	})
	if s.hub != nil && providerID != "" && snap.CommissionExpected != nil {
		s.hub.SendToUser(providerID, ws.NewEvent(ws.EventCommissionBooked, ws.CommissionBookedPayload{
			TransactionID: snap.TransactionID,
			GrossAmount:   amount,
			Commission:    *snap.CommissionExpected,
		}))
	}

	s.log.Info().
		Str("booking_id", snap.BookingID).
		Str("transaction_id", snap.TransactionID).
		Float64("amount", amount).
		Msg("provider confirmed payment — txn confirmed + commission booked")

	return snap, nil
}

// bookCommissionOnPaid writes commission_ledger commission_debit (matches provider_balances view).
func (s *JobLifecycleService) bookCommissionOnPaid(ctx context.Context, snap JobSnapshot) {
	if s.pool == nil {
		return
	}
	amount := 0.0
	if snap.BillAmount != nil {
		amount = *snap.BillAmount
	}
	if amount <= 0 {
		_ = s.pool.QueryRow(ctx, `select coalesce(bill_amount,0) from bookings where id = $1::uuid`, snap.BookingID).Scan(&amount)
	}
	if amount <= 0 && snap.ServiceRequestID != "" {
		_ = s.pool.QueryRow(ctx, `
			select coalesce(amount,0) from service_transactions
			where request_id::text = $1 or service_request_id::text = $1 or booking_id::text = $2
			order by created_at desc nulls last limit 1
		`, snap.ServiceRequestID, snap.BookingID).Scan(&amount)
	}
	if amount <= 0 {
		s.log.Warn().Str("booking_id", snap.BookingID).Msg("no bill amount — skip commission")
		return
	}
	commission := CommissionOn(amount)
	providerID := snap.ProviderID
	if providerID == "" {
		var pid string
		_ = s.pool.QueryRow(ctx, `
			select coalesce(
				(select g.owner_id::text from garages g join bookings b on b.garage_id = g.id where b.id = $1::uuid),
				(select m.profile_id::text from mechanics m join bookings b on b.mechanic_id = m.id where b.id = $1::uuid)
			)
		`, snap.BookingID).Scan(&pid)
		providerID = pid
	}
	if providerID == "" {
		s.log.Warn().Str("booking_id", snap.BookingID).Msg("no provider_id — skip commission")
		return
	}

	// Idempotent: already booked for this transaction or booking note
	var exists bool
	_ = s.pool.QueryRow(ctx, `
		select exists(
			select 1 from commission_ledger
			where entry_type = 'commission_debit'
			  and (
			    ($1 <> '' and transaction_id::text = $1)
			    or note like '%' || $2 || '%'
			  )
		)
	`, snap.TransactionID, snap.BookingID).Scan(&exists)
	if exists {
		s.log.Info().Str("booking_id", snap.BookingID).Msg("commission already booked")
		return
	}

	var txnUUID *uuid.UUID
	if snap.TransactionID != "" {
		if tid, e := uuid.Parse(snap.TransactionID); e == nil {
			txnUUID = &tid
		}
	}

	_, err := s.pool.Exec(ctx, `
		insert into commission_ledger (
			id, provider_id, entry_type, amount, currency,
			transaction_id, commission_rate, gross_amount, period_month, note, created_at
		) values (
			gen_random_uuid(), $1::uuid, 'commission_debit', $2, 'TZS',
			$3, $4, $5, date_trunc('month', now())::date,
			$6, now()
		)
		on conflict (transaction_id, entry_type) do nothing
	`, providerID, commission, txnUUID, CommissionRate, amount,
		"Commission on booking "+snap.BookingID+" (gross "+formatAmount(amount)+")")
	if err != nil {
		// Without unique constraint / optional columns
		_, err2 := s.pool.Exec(ctx, `
			insert into commission_ledger (provider_id, entry_type, amount, currency, note, created_at)
			values ($1::uuid, 'commission_debit', $2, 'TZS', $3, now())
		`, providerID, commission, "Commission booking "+snap.BookingID)
		if err2 != nil {
			s.log.Warn().Err(err).Err(err2).Msg("commission_ledger insert failed")
			return
		}
	}
	s.log.Info().
		Str("provider_id", providerID).
		Float64("gross", amount).
		Float64("commission", commission).
		Msg("commission_debit booked — provider_balances will update")
}

func formatAmount(v float64) string {
	return fmt.Sprintf("%.2f", v)
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


// ProviderDeny terminates a pending/accepted job with optional feedback.
func (s *JobLifecycleService) ProviderDeny(ctx context.Context, bookingID uuid.UUID, reason string) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	_, _ = s.pool.Exec(ctx, `
		update bookings set status = 'cancelled', provider_feedback = $2, updated_at = now() where id = $1
	`, bookingID, reason)
	if snap.ServiceRequestID != "" {
		_, _ = s.pool.Exec(ctx, `
			update service_requests set status = 'cancelled', decline_reason = $2, updated_at = now()
			where id = $1::uuid
		`, snap.ServiceRequestID, reason)
	}
	snap.Phase = PhaseCancelled
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseCancelled,
	})
	return snap, nil
}

// CustomerUnsatisfied — customer rejects work; provider must restart or job fails.
func (s *JobLifecycleService) CustomerUnsatisfied(ctx context.Context, bookingID uuid.UUID, note string) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	_, _ = s.pool.Exec(ctx, `
		update bookings set status = 'needs_rework', customer_satisfied = false, updated_at = now() where id = $1
	`, bookingID)
	snap.Phase = "needs_rework"
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           "needs_rework",
	})
	return snap, nil
}

// RestartService — provider restarts after needs_rework.
func (s *JobLifecycleService) RestartService(ctx context.Context, bookingID uuid.UUID) (JobSnapshot, error) {
	snap, err := s.load(ctx, bookingID)
	if err != nil {
		return snap, err
	}
	_, _ = s.pool.Exec(ctx, `
		update bookings set status = 'in_progress', started_at = coalesce(started_at, now()), updated_at = now() where id = $1
	`, bookingID)
	snap.Phase = PhaseInProgress
	s.syncRequest(ctx, snap.ServiceRequestID, PhaseInProgress)
	s.notify(snap.CarOwnerID, snap.ProviderID, ws.StatusUpdatePayload{
		ServiceRequestID: snap.ServiceRequestID,
		BookingID:        snap.BookingID,
		Status:           PhaseInProgress,
	})
	return snap, nil
}
