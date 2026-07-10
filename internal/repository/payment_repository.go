package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type PaymentRepository interface {
	Create(ctx context.Context, bookingID uuid.UUID, amount, currency, txRef string) (models.Payment, error)
	GetByBooking(ctx context.Context, bookingID uuid.UUID) (models.Payment, error)
	GetByTxRef(ctx context.Context, txRef string) (models.Payment, error)
	MarkSettled(ctx context.Context, txRef, status string, providerTxnID *string, rawPayload json.RawMessage) error
}

type paymentRepository struct {
	q *sqlcgen.Queries
}

func NewPaymentRepository(q *sqlcgen.Queries) PaymentRepository {
	return &paymentRepository{q: q}
}

func (r *paymentRepository) Create(ctx context.Context, bookingID uuid.UUID, amount, currency, txRef string) (models.Payment, error) {
	row, err := r.q.CreatePayment(ctx, sqlcgen.CreatePaymentParams{
		BookingID: bookingID, Amount: amount, Currency: currency, ProviderTxRef: txRef,
	})
	if err != nil {
		return models.Payment{}, err
	}
	return models.Payment{ID: row.ID, BookingID: bookingID, Amount: amount, Currency: currency, Status: row.Status, ProviderTxRef: row.ProviderTxRef, CreatedAt: row.CreatedAt}, nil
}

func (r *paymentRepository) GetByBooking(ctx context.Context, bookingID uuid.UUID) (models.Payment, error) {
	row, err := r.q.GetPaymentByBooking(ctx, bookingID)
	if err != nil {
		return models.Payment{}, err
	}
	return toPaymentModel(row), nil
}

func (r *paymentRepository) GetByTxRef(ctx context.Context, txRef string) (models.Payment, error) {
	row, err := r.q.GetPaymentByTxRef(ctx, txRef)
	if err != nil {
		return models.Payment{}, err
	}
	return toPaymentModel(row), nil
}

func (r *paymentRepository) MarkSettled(ctx context.Context, txRef, status string, providerTxnID *string, rawPayload json.RawMessage) error {
	return r.q.MarkPaymentSettled(ctx, txRef, status, providerTxnID, rawPayload)
}

func toPaymentModel(row sqlcgen.Payment) models.Payment {
	return models.Payment{
		ID: row.ID, BookingID: row.BookingID, Amount: row.Amount, Currency: row.Currency,
		Status: row.Status, Provider: row.Provider, ProviderTxRef: row.ProviderTxRef,
		PaidAt: row.PaidAt, CreatedAt: row.CreatedAt,
	}
}
