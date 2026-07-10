package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type OfferRepository interface {
	Create(ctx context.Context, in models.CreateOfferInput) (uuid.UUID, string, error)
	ListForRequest(ctx context.Context, requestID uuid.UUID) ([]models.Offer, error)
	Get(ctx context.Context, id uuid.UUID) (models.Offer, error)
	// Accept runs the whole "accept an offer" step as one DB
	// transaction: reject the other pending offers on the same
	// request, create the booking row, and flip the request's status
	// to accepted — all-or-nothing, so a failure partway through never
	// leaves the request in an inconsistent state (e.g. two accepted
	// offers, or an accepted offer with no booking).
	Accept(ctx context.Context, offerID uuid.UUID) (models.AcceptOfferResult, error)
}

type offerRepository struct {
	pool *pgxpool.Pool
	q    *sqlcgen.Queries
}

func NewOfferRepository(pool *pgxpool.Pool, q *sqlcgen.Queries) OfferRepository {
	return &offerRepository{pool: pool, q: q}
}

func (r *offerRepository) Create(ctx context.Context, in models.CreateOfferInput) (uuid.UUID, string, error) {
	row, err := r.q.CreateOffer(ctx, sqlcgen.CreateOfferParams{
		ServiceRequestID: in.ServiceRequestID,
		GarageID:         in.GarageID,
		MechanicID:       in.MechanicID,
		Price:            in.Price,
		Currency:         in.Currency,
		EtaMinutes:       in.EtaMinutes,
		Notes:            in.Notes,
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return row.ID, row.Status, nil
}

func (r *offerRepository) ListForRequest(ctx context.Context, requestID uuid.UUID) ([]models.Offer, error) {
	rows, err := r.q.ListOffersForRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Offer, 0, len(rows))
	for _, row := range rows {
		out = append(out, toOfferModel(row))
	}
	return out, nil
}

func (r *offerRepository) Get(ctx context.Context, id uuid.UUID) (models.Offer, error) {
	row, err := r.q.GetOffer(ctx, id)
	if err != nil {
		return models.Offer{}, err
	}
	return toOfferModel(row), nil
}

func (r *offerRepository) Accept(ctx context.Context, offerID uuid.UUID) (models.AcceptOfferResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) // no-op if committed

	qtx := r.q.WithTx(tx)

	offer, err := qtx.GetOffer(ctx, offerID)
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("load offer: %w", err)
	}
	if offer.Status != "pending" {
		return models.AcceptOfferResult{}, fmt.Errorf("offer is not pending (status=%s)", offer.Status)
	}

	if err := qtx.SetOfferStatus(ctx, offerID, "accepted"); err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("accept offer: %w", err)
	}
	if err := qtx.RejectOtherOffers(ctx, offer.ServiceRequestID, offerID); err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("reject other offers: %w", err)
	}

	booking, err := qtx.CreateBooking(ctx, sqlcgen.CreateBookingParams{
		ServiceRequestID: offer.ServiceRequestID,
		OfferID:          offerID,
		GarageID:         offer.GarageID,
		MechanicID:       offer.MechanicID,
		ScheduledTime:    nil,
	})
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("create booking: %w", err)
	}

	if err := qtx.UpdateServiceRequestStatus(ctx, offer.ServiceRequestID, "accepted"); err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("update request status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("commit tx: %w", err)
	}

	return models.AcceptOfferResult{
		ServiceRequestID: offer.ServiceRequestID,
		OfferID:          offerID,
		BookingID:        booking.ID,
		GarageID:         offer.GarageID,
		MechanicID:       offer.MechanicID,
	}, nil
}

func toOfferModel(row sqlcgen.Offer) models.Offer {
	return models.Offer{
		ID:               row.ID,
		ServiceRequestID: row.ServiceRequestID,
		GarageID:         row.GarageID,
		MechanicID:       row.MechanicID,
		Price:            row.Price,
		Currency:         row.Currency,
		EtaMinutes:       row.EtaMinutes,
		Notes:            row.Notes,
		Status:           row.Status,
		CreatedAt:        row.CreatedAt,
	}
}
