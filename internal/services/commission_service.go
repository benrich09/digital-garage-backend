package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
)

// CommissionRate is the platform's cut of every confirmed job: 5%.
//
// This constant is the DEFAULT for new transactions only. The rate is
// frozen onto each ledger entry at write time (see the
// book_commission_on_confirm trigger), so changing this number never
// rewrites what a provider already owes.
const CommissionRate = 0.10

// SettlementDueDay is the day of the following month by which a provider
// must have settled the previous month's commission.
const SettlementDueDay = 7

var (
	ErrNotCarOwner      = errors.New("only the car owner may confirm this transaction")
	ErrAlreadyConfirmed = errors.New("transaction is already confirmed")
	ErrNotConfirmable   = errors.New("transaction is cancelled or disputed and cannot be confirmed")
	ErrSettlementClosed = errors.New("settlement has already been verified")
)

// CommissionService owns the post-service money trail. Note what it does
// NOT do: it never moves money. The car owner pays the provider directly
// (cash or their own mobile money) and the platform only records what
// happened and what it is therefore owed.
type CommissionService struct {
	txns        repository.ServiceTransactionRepository
	ledger      repository.CommissionLedgerRepository
	settlements repository.SettlementRepository
	hub         *ws.Manager
	log         zerolog.Logger
}

func NewCommissionService(
	txns repository.ServiceTransactionRepository,
	ledger repository.CommissionLedgerRepository,
	settlements repository.SettlementRepository,
	hub *ws.Manager,
	log zerolog.Logger,
) *CommissionService {
	return &CommissionService{txns: txns, ledger: ledger, settlements: settlements, hub: hub, log: log}
}

// CommissionOn returns the platform's cut of a gross amount, rounded to
// two decimals.
func CommissionOn(amount float64) float64 {
	return math.Round(amount*CommissionRate*100) / 100
}

// RecordService is called by the PROVIDER once work is done and they
// have been paid directly. It creates a transaction awaiting the car
// owner's confirmation — it does not yet book any commission, because a
// provider asserting their own sale is not evidence of one.
func (s *CommissionService) RecordService(
	ctx context.Context, providerID uuid.UUID, in models.RecordServiceInput,
) (models.ServiceTransaction, error) {
	if in.Amount <= 0 {
		return models.ServiceTransaction{}, fmt.Errorf("amount must be greater than zero")
	}
	if in.ServiceName == "" {
		return models.ServiceTransaction{}, fmt.Errorf("service_name is required")
	}

	txn, err := s.txns.Create(ctx, models.ServiceTransaction{
		ProviderID:  providerID,
		CarOwnerID:  in.CarOwnerID,
		BookingID:   in.BookingID,
		RequestID:   in.RequestID,
		GarageID:    in.GarageID,
		ServiceID:   in.ServiceID,
		ServiceName: in.ServiceName,
		Amount:      in.Amount,
		Currency:    defaultCurrency(in.Currency),
		PaidMethod:  in.PaidMethod,
		Status:      models.TxnAwaitingConfirmation,
		CreatedAt:   time.Now().UTC(),
	})
	if err != nil {
		return models.ServiceTransaction{}, fmt.Errorf("create transaction: %w", err)
	}

	// The car owner needs to act, so push to them, not the provider.
	s.hub.SendToUser(in.CarOwnerID.String(), ws.NewEvent(ws.EventConfirmationRequested, ws.ConfirmationRequestedPayload{
		TransactionID: txn.ID.String(),
		ServiceName:   txn.ServiceName,
		Amount:        txn.Amount,
		Currency:      txn.Currency,
	}))

	s.log.Info().
		Str("transaction_id", txn.ID.String()).
		Str("provider_id", providerID.String()).
		Float64("amount", txn.Amount).
		Msg("service recorded, awaiting car owner confirmation")

	return txn, nil
}

// Confirm is the CAR OWNER attesting they paid the provider. This is the
// single event that books commission — the DB trigger writes the ledger
// debit, so the commission cannot be bypassed by any code path that
// forgets to call this service.
func (s *CommissionService) Confirm(
	ctx context.Context, callerID, txnID uuid.UUID,
) (models.ServiceTransaction, error) {
	txn, err := s.txns.GetByID(ctx, txnID)
	if err != nil {
		return models.ServiceTransaction{}, fmt.Errorf("load transaction: %w", err)
	}
	if txn.CarOwnerID != callerID {
		return models.ServiceTransaction{}, ErrNotCarOwner
	}

	switch txn.Status {
	case models.TxnConfirmed:
		return models.ServiceTransaction{}, ErrAlreadyConfirmed
	case models.TxnCancelled, models.TxnDisputed:
		return models.ServiceTransaction{}, ErrNotConfirmable
	}

	now := time.Now().UTC()
	confirmed, err := s.txns.MarkConfirmed(ctx, txnID, callerID, now)
	if err != nil {
		return models.ServiceTransaction{}, fmt.Errorf("confirm transaction: %w", err)
	}

	commission := CommissionOn(confirmed.Amount)

	s.log.Info().
		Str("transaction_id", txnID.String()).
		Float64("gross", confirmed.Amount).
		Float64("commission", commission).
		Msg("transaction confirmed, commission booked")

	// Tell the provider their balance moved, so the home dashboard
	// updates without a manual refresh.
	s.hub.SendToUser(confirmed.ProviderID.String(), ws.NewEvent(ws.EventCommissionBooked, ws.CommissionBookedPayload{
		TransactionID: txnID.String(),
		GrossAmount:   confirmed.Amount,
		Commission:    commission,
	}))

	return confirmed, nil
}

// Dispute lets the car owner reject a transaction they did not pay for.
// Disputed rows are excluded from the ledger rather than deleted, so a
// provider raising false transactions leaves a visible trail.
func (s *CommissionService) Dispute(
	ctx context.Context, callerID, txnID uuid.UUID, reason string,
) error {
	txn, err := s.txns.GetByID(ctx, txnID)
	if err != nil {
		return fmt.Errorf("load transaction: %w", err)
	}
	if txn.CarOwnerID != callerID {
		return ErrNotCarOwner
	}
	if txn.Status == models.TxnConfirmed {
		// Already booked. Reversing revenue is a finance decision, not a
		// customer-facing one.
		return fmt.Errorf("transaction already confirmed; contact support to reverse")
	}

	if err := s.txns.MarkDisputed(ctx, txnID, reason, time.Now().UTC()); err != nil {
		return fmt.Errorf("dispute transaction: %w", err)
	}

	s.log.Warn().
		Str("transaction_id", txnID.String()).
		Str("reason", reason).
		Msg("transaction disputed by car owner")
	return nil
}

// Balance is what the provider home dashboard shows: not their earnings,
// but what they owe the platform.
type Balance struct {
	BalanceOwed       float64   `json:"balance_owed"`
	CommissionCharged float64   `json:"commission_charged"`
	CommissionSettled float64   `json:"commission_settled"`
	GrossRevenue      float64   `json:"gross_revenue"`
	JobsBilled        int64     `json:"jobs_billed"`
	CurrentPeriodDue  float64   `json:"current_period_due"`
	NextDueDate       time.Time `json:"next_due_date"`
}

func (s *CommissionService) BalanceFor(ctx context.Context, providerID uuid.UUID) (Balance, error) {
	b, err := s.ledger.BalanceFor(ctx, providerID)
	if err != nil {
		return Balance{}, fmt.Errorf("load balance: %w", err)
	}

	month := time.Now().UTC()
	periodDue, err := s.ledger.ChargedInMonth(ctx, providerID, month)
	if err != nil {
		return Balance{}, fmt.Errorf("load period total: %w", err)
	}

	return Balance{
		BalanceOwed:       b.BalanceOwed,
		CommissionCharged: b.CommissionCharged,
		CommissionSettled: b.CommissionSettled,
		GrossRevenue:      b.GrossRevenue,
		JobsBilled:        b.JobsBilled,
		CurrentPeriodDue:  periodDue,
		NextDueDate:       NextSettlementDue(month),
	}, nil
}

// NextSettlementDue returns the deadline for settling the given month's
// commission: the 7th of the following month.
func NextSettlementDue(month time.Time) time.Time {
	y, m := month.Year(), month.Month()
	return time.Date(y, m+1, SettlementDueDay, 23, 59, 59, 0, time.UTC)
}

// SubmitSettlement is the provider reporting that they have paid their
// outstanding commission into the platform's account. It does NOT credit
// the ledger — an admin must verify the reference first, because the
// payment did not pass through our systems and we have no other way to
// know it arrived.
func (s *CommissionService) SubmitSettlement(
	ctx context.Context, providerID uuid.UUID, settlementID uuid.UUID, reference, method string,
) error {
	st, err := s.settlements.GetByID(ctx, settlementID)
	if err != nil {
		return fmt.Errorf("load settlement: %w", err)
	}
	if st.ProviderID != providerID {
		return errors.New("settlement does not belong to this provider")
	}
	if st.Status == models.SettlementVerified {
		return ErrSettlementClosed
	}
	if reference == "" {
		return errors.New("payment reference is required")
	}

	if err := s.settlements.MarkSubmitted(ctx, settlementID, reference, method, time.Now().UTC()); err != nil {
		return fmt.Errorf("submit settlement: %w", err)
	}

	s.log.Info().
		Str("settlement_id", settlementID.String()).
		Str("provider_id", providerID.String()).
		Msg("settlement submitted, awaiting admin verification")
	return nil
}

// VerifySettlement is the ADMIN action that actually clears the debt. It
// writes the credit entry, which is the only thing that reduces a
// provider's balance.
func (s *CommissionService) VerifySettlement(
	ctx context.Context, adminID, settlementID uuid.UUID,
) error {
	st, err := s.settlements.GetByID(ctx, settlementID)
	if err != nil {
		return fmt.Errorf("load settlement: %w", err)
	}
	if st.Status == models.SettlementVerified {
		return ErrSettlementClosed
	}

	now := time.Now().UTC()
	if err := s.settlements.MarkVerified(ctx, settlementID, adminID, now); err != nil {
		return fmt.Errorf("verify settlement: %w", err)
	}

	// Credit is stored negative so that balance = sum(amount).
	if err := s.ledger.WriteCredit(ctx, st.ProviderID, settlementID, -st.AmountDue, st.PeriodMonth, now); err != nil {
		return fmt.Errorf("write settlement credit: %w", err)
	}

	s.hub.SendToUser(st.ProviderID.String(), ws.NewEvent(ws.EventSettlementVerified, ws.SettlementVerifiedPayload{
		SettlementID: settlementID.String(),
		Amount:       st.AmountDue,
	}))

	s.log.Info().
		Str("settlement_id", settlementID.String()).
		Float64("amount", st.AmountDue).
		Msg("settlement verified, ledger credited")
	return nil
}

// ListDebtors backs the admin Debts page.
func (s *CommissionService) ListDebtors(ctx context.Context) ([]models.LedgerBalance, error) {
	return s.ledger.ListDebtors(ctx, 100)
}

func defaultCurrency(c string) string {
	if c == "" {
		return "TZS"
	}
	return c
}
