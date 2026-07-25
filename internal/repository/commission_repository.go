package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/models"
)

// These three repositories query the pool directly rather than going
// through sqlcgen. That's a deliberate exception to the pattern used by
// the older repositories: the commission tables arrived in migration
// 0013, and wiring them through sqlc would mean nobody can compile the
// service until `sqlc generate` has been run against a live schema.
// Direct pgx keeps the build self-contained. If you later add these to
// db/queries/*.sql and regenerate, swap the bodies — the interfaces are
// what the service depends on and they won't change.

type ServiceTransactionRepository interface {
	Create(ctx context.Context, in models.ServiceTransaction) (models.ServiceTransaction, error)
	GetByID(ctx context.Context, id uuid.UUID) (models.ServiceTransaction, error)
	MarkConfirmed(ctx context.Context, id, confirmedBy uuid.UUID, at time.Time) (models.ServiceTransaction, error)
	MarkDisputed(ctx context.Context, id uuid.UUID, reason string, at time.Time) error
	ListForProvider(ctx context.Context, providerID uuid.UUID, limit int32) ([]models.ServiceTransaction, error)
	ListForCarOwner(ctx context.Context, carOwnerID uuid.UUID, limit int32) ([]models.ServiceTransaction, error)
}

type serviceTransactionRepository struct{ pool *pgxpool.Pool }

func NewServiceTransactionRepository(pool *pgxpool.Pool) ServiceTransactionRepository {
	return &serviceTransactionRepository{pool: pool}
}

const txnColumns = `id, booking_id, request_id, car_owner_id, provider_id, garage_id,
	service_id, service_name, amount, currency, coalesce(paid_method, ''), status,
	confirmed_at, confirmed_by, created_at`

func scanTxn(row pgx.Row) (models.ServiceTransaction, error) {
	var t models.ServiceTransaction
	err := row.Scan(&t.ID, &t.BookingID, &t.RequestID, &t.CarOwnerID, &t.ProviderID,
		&t.GarageID, &t.ServiceID, &t.ServiceName, &t.Amount, &t.Currency,
		&t.PaidMethod, &t.Status, &t.ConfirmedAt, &t.ConfirmedBy, &t.CreatedAt)
	return t, err
}

func (r *serviceTransactionRepository) Create(ctx context.Context, in models.ServiceTransaction) (models.ServiceTransaction, error) {
	row := r.pool.QueryRow(ctx, `
		insert into service_transactions
			(booking_id, request_id, car_owner_id, provider_id, garage_id,
			 service_id, service_name, amount, currency, paid_method, status)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,''),$11)
		returning `+txnColumns,
		in.BookingID, in.RequestID, in.CarOwnerID, in.ProviderID, in.GarageID,
		in.ServiceID, in.ServiceName, in.Amount, in.Currency, in.PaidMethod, in.Status)
	return scanTxn(row)
}

func (r *serviceTransactionRepository) GetByID(ctx context.Context, id uuid.UUID) (models.ServiceTransaction, error) {
	row := r.pool.QueryRow(ctx, `select `+txnColumns+` from service_transactions where id = $1`, id)
	t, err := scanTxn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errors.New("transaction not found")
	}
	return t, err
}

// MarkConfirmed sets the status; the book_commission_on_confirm trigger
// writes the ledger debit as part of the same statement. The guard on
// status makes a replayed confirm a no-op rather than a second charge.
func (r *serviceTransactionRepository) MarkConfirmed(ctx context.Context, id, confirmedBy uuid.UUID, at time.Time) (models.ServiceTransaction, error) {
	row := r.pool.QueryRow(ctx, `
		update service_transactions
		set status = 'confirmed', confirmed_at = $2, confirmed_by = $3
		where id = $1 and status = 'awaiting_confirmation'
		returning `+txnColumns, id, at, confirmedBy)
	t, err := scanTxn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, errors.New("transaction is not awaiting confirmation")
	}
	return t, err
}

func (r *serviceTransactionRepository) MarkDisputed(ctx context.Context, id uuid.UUID, reason string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		update service_transactions
		set status = 'disputed', disputed_at = $2, dispute_reason = $3
		where id = $1 and status = 'awaiting_confirmation'`, id, at, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("transaction is not awaiting confirmation")
	}
	return nil
}

func (r *serviceTransactionRepository) listBy(ctx context.Context, column string, id uuid.UUID, limit int32) ([]models.ServiceTransaction, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// column is never user-supplied — the two call sites below pass
	// literals, so this concatenation cannot be injected into.
	rows, err := r.pool.Query(ctx,
		`select `+txnColumns+` from service_transactions where `+column+` = $1
		 order by created_at desc limit $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.ServiceTransaction, 0, limit)
	for rows.Next() {
		t, err := scanTxn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *serviceTransactionRepository) ListForProvider(ctx context.Context, providerID uuid.UUID, limit int32) ([]models.ServiceTransaction, error) {
	return r.listBy(ctx, "provider_id", providerID, limit)
}

func (r *serviceTransactionRepository) ListForCarOwner(ctx context.Context, carOwnerID uuid.UUID, limit int32) ([]models.ServiceTransaction, error) {
	return r.listBy(ctx, "car_owner_id", carOwnerID, limit)
}

// ---------------------------------------------------------------------

type CommissionLedgerRepository interface {
	BalanceFor(ctx context.Context, providerID uuid.UUID) (models.LedgerBalance, error)
	ChargedInMonth(ctx context.Context, providerID uuid.UUID, month time.Time) (float64, error)
	WriteCredit(ctx context.Context, providerID, settlementID uuid.UUID, amount float64, period time.Time, at time.Time) error
	ListDebtors(ctx context.Context, limit int32) ([]models.LedgerBalance, error)
}

type commissionLedgerRepository struct{ pool *pgxpool.Pool }

func NewCommissionLedgerRepository(pool *pgxpool.Pool) CommissionLedgerRepository {
	return &commissionLedgerRepository{pool: pool}
}

func (r *commissionLedgerRepository) BalanceFor(ctx context.Context, providerID uuid.UUID) (models.LedgerBalance, error) {
	var b models.LedgerBalance
	err := r.pool.QueryRow(ctx, `
		select provider_id, commission_charged, commission_settled,
		       balance_owed, gross_revenue, jobs_billed
		from provider_balances where provider_id = $1`, providerID).
		Scan(&b.ProviderID, &b.CommissionCharged, &b.CommissionSettled,
			&b.BalanceOwed, &b.GrossRevenue, &b.JobsBilled)
	if errors.Is(err, pgx.ErrNoRows) {
		// A provider with no jobs has no row in the view. Zeroes are the
		// correct answer, not an error.
		return models.LedgerBalance{ProviderID: providerID}, nil
	}
	return b, err
}

func (r *commissionLedgerRepository) ChargedInMonth(ctx context.Context, providerID uuid.UUID, month time.Time) (float64, error) {
	var total float64
	err := r.pool.QueryRow(ctx, `
		select coalesce(sum(amount), 0) from commission_ledger
		where provider_id = $1 and entry_type = 'commission_debit'
		  and period_month = date_trunc('month', $2::timestamptz)::date`,
		providerID, month).Scan(&total)
	return total, err
}

func (r *commissionLedgerRepository) WriteCredit(ctx context.Context, providerID, settlementID uuid.UUID, amount float64, period time.Time, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		insert into commission_ledger
			(provider_id, entry_type, amount, settlement_id, period_month, created_at, note)
		values ($1, 'settlement_credit', $2, $3, date_trunc('month', $4::timestamptz)::date, $5, 'Monthly settlement')`,
		providerID, amount, settlementID, period, at)
	return err
}

// ListDebtors backs the admin Debts page: who owes us, most first.
func (r *commissionLedgerRepository) ListDebtors(ctx context.Context, limit int32) ([]models.LedgerBalance, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		select provider_id, commission_charged, commission_settled,
		       balance_owed, gross_revenue, jobs_billed
		from provider_balances
		where balance_owed > 0
		order by balance_owed desc limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.LedgerBalance
	for rows.Next() {
		var b models.LedgerBalance
		if err := rows.Scan(&b.ProviderID, &b.CommissionCharged, &b.CommissionSettled,
			&b.BalanceOwed, &b.GrossRevenue, &b.JobsBilled); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------

type SettlementRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Settlement, error)
	ListForProvider(ctx context.Context, providerID uuid.UUID) ([]models.Settlement, error)
	ListByStatus(ctx context.Context, status string, limit int32) ([]models.Settlement, error)
	MarkSubmitted(ctx context.Context, id uuid.UUID, reference, method string, at time.Time) error
	MarkVerified(ctx context.Context, id, adminID uuid.UUID, at time.Time) error
	GenerateForMonth(ctx context.Context, month time.Time, dueDate time.Time) (int64, error)
}

type settlementRepository struct{ pool *pgxpool.Pool }

func NewSettlementRepository(pool *pgxpool.Pool) SettlementRepository {
	return &settlementRepository{pool: pool}
}

const settlementColumns = `id, provider_id, period_month, amount_due, currency, status,
	coalesce(paid_reference,''), coalesce(paid_method,''), submitted_at, verified_at, due_date, created_at`

func scanSettlement(row pgx.Row) (models.Settlement, error) {
	var s models.Settlement
	err := row.Scan(&s.ID, &s.ProviderID, &s.PeriodMonth, &s.AmountDue, &s.Currency,
		&s.Status, &s.PaidReference, &s.PaidMethod, &s.SubmittedAt, &s.VerifiedAt,
		&s.DueDate, &s.CreatedAt)
	return s, err
}

func (r *settlementRepository) GetByID(ctx context.Context, id uuid.UUID) (models.Settlement, error) {
	row := r.pool.QueryRow(ctx, `select `+settlementColumns+` from provider_settlements where id = $1`, id)
	s, err := scanSettlement(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, errors.New("settlement not found")
	}
	return s, err
}

func (r *settlementRepository) ListForProvider(ctx context.Context, providerID uuid.UUID) ([]models.Settlement, error) {
	rows, err := r.pool.Query(ctx,
		`select `+settlementColumns+` from provider_settlements
		 where provider_id = $1 order by period_month desc limit 24`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSettlements(rows)
}

func (r *settlementRepository) ListByStatus(ctx context.Context, status string, limit int32) ([]models.Settlement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx,
		`select `+settlementColumns+` from provider_settlements
		 where ($1 = '' or status = $1::settlement_status)
		 order by due_date asc limit $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectSettlements(rows)
}

func collectSettlements(rows pgx.Rows) ([]models.Settlement, error) {
	var out []models.Settlement
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkSubmitted records the provider's payment reference. Note it does
// NOT touch amount_due — the provider must not be able to decide what
// they owe.
func (r *settlementRepository) MarkSubmitted(ctx context.Context, id uuid.UUID, reference, method string, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		update provider_settlements
		set status = 'submitted', paid_reference = $2, paid_method = nullif($3,''), submitted_at = $4
		where id = $1 and status <> 'verified'`, id, reference, method, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("settlement already verified")
	}
	return nil
}

func (r *settlementRepository) MarkVerified(ctx context.Context, id, adminID uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		update provider_settlements
		set status = 'verified', verified_at = $2, verified_by = $3
		where id = $1 and status <> 'verified'`, id, at, adminID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("settlement already verified")
	}
	return nil
}

// GenerateForMonth creates one settlement row per provider who accrued
// commission in the given month. Idempotent via the
// (provider_id, period_month) unique constraint, so running it twice —
// or re-running a failed month-end job — is safe.
func (r *settlementRepository) GenerateForMonth(ctx context.Context, month time.Time, dueDate time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		insert into provider_settlements (provider_id, period_month, amount_due, due_date)
		select l.provider_id,
		       date_trunc('month', $1::timestamptz)::date,
		       sum(l.amount),
		       $2::date
		from commission_ledger l
		where l.entry_type = 'commission_debit'
		  and l.period_month = date_trunc('month', $1::timestamptz)::date
		group by l.provider_id
		having sum(l.amount) > 0
		on conflict (provider_id, period_month) do nothing`, month, dueDate)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
