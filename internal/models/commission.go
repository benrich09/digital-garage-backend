package models

import (
	"time"

	"github.com/google/uuid"
)

// Transaction lifecycle. Mirrors the txn_status enum in migration 0013.
const (
	TxnAwaitingConfirmation = "awaiting_confirmation"
	TxnConfirmed            = "confirmed"
	TxnDisputed             = "disputed"
	TxnCancelled            = "cancelled"
)

// Settlement lifecycle. Mirrors the settlement_status enum.
const (
	SettlementDue       = "due"
	SettlementSubmitted = "submitted"
	SettlementVerified  = "verified"
	SettlementRejected  = "rejected"
	SettlementOverdue   = "overdue"
)

// ServiceTransaction is a completed job the customer paid for directly.
// The platform is not a party to that payment — this row exists so both
// sides have a record and so the 5% commission has something to attach
// to once the car owner confirms.
type ServiceTransaction struct {
	ID          uuid.UUID  `json:"id"`
	BookingID   *uuid.UUID `json:"booking_id,omitempty"`
	RequestID   *uuid.UUID `json:"request_id,omitempty"`
	CarOwnerID  uuid.UUID  `json:"car_owner_id"`
	ProviderID  uuid.UUID  `json:"provider_id"`
	GarageID    *uuid.UUID `json:"garage_id,omitempty"`
	ServiceID   *uuid.UUID `json:"service_id,omitempty"`
	ServiceName string     `json:"service_name"`
	Amount      float64    `json:"amount"`
	Currency    string     `json:"currency"`
	PaidMethod  string     `json:"paid_method,omitempty"`
	Status      string     `json:"status"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	ConfirmedBy *uuid.UUID `json:"confirmed_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// RecordServiceInput is what a provider posts after finishing a job.
type RecordServiceInput struct {
	CarOwnerID  uuid.UUID  `json:"car_owner_id"`
	BookingID   *uuid.UUID `json:"booking_id,omitempty"`
	RequestID   *uuid.UUID `json:"request_id,omitempty"`
	GarageID    *uuid.UUID `json:"garage_id,omitempty"`
	ServiceID   *uuid.UUID `json:"service_id,omitempty"`
	ServiceName string     `json:"service_name"`
	Amount      float64    `json:"amount"`
	Currency    string     `json:"currency,omitempty"`
	PaidMethod  string     `json:"paid_method,omitempty"`
}

// DisputeInput is the car owner rejecting a transaction they didn't pay.
type DisputeInput struct {
	Reason string `json:"reason"`
}

// LedgerBalance is the provider_balances view.
type LedgerBalance struct {
	ProviderID        uuid.UUID `json:"provider_id"`
	CommissionCharged float64   `json:"commission_charged"`
	CommissionSettled float64   `json:"commission_settled"`
	BalanceOwed       float64   `json:"balance_owed"`
	GrossRevenue      float64   `json:"gross_revenue"`
	JobsBilled        int64     `json:"jobs_billed"`
}

// Settlement is one provider's monthly commission bill.
type Settlement struct {
	ID            uuid.UUID  `json:"id"`
	ProviderID    uuid.UUID  `json:"provider_id"`
	PeriodMonth   time.Time  `json:"period_month"`
	AmountDue     float64    `json:"amount_due"`
	Currency      string     `json:"currency"`
	Status        string     `json:"status"`
	PaidReference string     `json:"paid_reference,omitempty"`
	PaidMethod    string     `json:"paid_method,omitempty"`
	SubmittedAt   *time.Time `json:"submitted_at,omitempty"`
	VerifiedAt    *time.Time `json:"verified_at,omitempty"`
	DueDate       time.Time  `json:"due_date"`
	CreatedAt     time.Time  `json:"created_at"`
}

// SubmitSettlementInput is the provider reporting they paid us.
type SubmitSettlementInput struct {
	Reference string `json:"reference"`
	Method    string `json:"method,omitempty"`
}
