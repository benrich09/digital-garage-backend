// Package repository is the only layer allowed to import sqlcgen.
// Services depend on the interfaces defined here, never on sqlcgen
// directly — that keeps the query-generation detail swappable and
// makes services trivially mockable in tests (no DB needed).
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type GarageRepository interface {
	ListNearby(ctx context.Context, lat, lng, radiusMeters float64, limit int32) ([]models.Garage, error)
	ListNearbyOfferingCategory(ctx context.Context, lat, lng, radiusMeters float64, categoryID uuid.UUID, limit int32) ([]models.Garage, error)
	GetByID(ctx context.Context, id uuid.UUID) (models.Garage, error)
	Create(ctx context.Context, ownerID uuid.UUID, in models.GarageVerificationInput) (uuid.UUID, error)
	AddServiceCategory(ctx context.Context, garageID, categoryID uuid.UUID) error
	ListPending(ctx context.Context) ([]models.PendingGarage, error)
	SetVerificationStatus(ctx context.Context, id, reviewerID uuid.UUID, status string) error
	Deactivate(ctx context.Context, id uuid.UUID) error
	DeleteMechanic(ctx context.Context, id uuid.UUID) error
}

type garageRepository struct {
	q *sqlcgen.Queries
}

func NewGarageRepository(q *sqlcgen.Queries) GarageRepository {
	return &garageRepository{q: q}
}

func (r *garageRepository) ListNearby(ctx context.Context, lat, lng, radiusMeters float64, limit int32) ([]models.Garage, error) {
	rows, err := r.q.ListNearbyGarages(ctx, sqlcgen.ListNearbyGaragesParams{
		Lat: lat, Lng: lng, RadiusMeters: radiusMeters, MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]models.Garage, 0, len(rows))
	for _, row := range rows {
		distKM := row.DistanceMeters / 1000.0
		out = append(out, models.Garage{
			ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Address: row.Address,
			IsVerified: row.IsVerified, Latitude: row.Latitude, Longitude: row.Longitude,
			DistanceKM: &distKM,
		})
	}
	return out, nil
}

func (r *garageRepository) ListNearbyOfferingCategory(ctx context.Context, lat, lng, radiusMeters float64, categoryID uuid.UUID, limit int32) ([]models.Garage, error) {
	rows, err := r.q.ListNearbyGaragesOfferingCategory(ctx, sqlcgen.ListNearbyGaragesOfferingCategoryParams{
		Lat: lat, Lng: lng, CategoryID: categoryID, RadiusMeters: radiusMeters, MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]models.Garage, 0, len(rows))
	for _, row := range rows {
		distKM := row.DistanceMeters / 1000.0
		out = append(out, models.Garage{
			ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Address: row.Address,
			IsVerified: row.IsVerified, Latitude: row.Latitude, Longitude: row.Longitude,
			DistanceKM: &distKM,
		})
	}
	return out, nil
}

func (r *garageRepository) GetByID(ctx context.Context, id uuid.UUID) (models.Garage, error) {
	row, err := r.q.GetGarage(ctx, id)
	if err != nil {
		return models.Garage{}, err
	}
	return models.Garage{
		ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Address: row.Address,
		IsVerified: row.IsVerified, Latitude: row.Latitude, Longitude: row.Longitude,
	}, nil
}

func (r *garageRepository) Create(ctx context.Context, ownerID uuid.UUID, in models.GarageVerificationInput) (uuid.UUID, error) {
	desc := ""
	addr := in.Address
	lic := in.LicenseNumber
	row, err := r.q.CreateGarage(ctx, sqlcgen.CreateGarageParams{
		OwnerID: ownerID, Name: in.Name, Description: &desc, Address: &addr,
		Phone: nil, LicenseNumber: &lic, Lat: in.Latitude, Lng: in.Longitude,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return row.ID, nil
}

func (r *garageRepository) AddServiceCategory(ctx context.Context, garageID, categoryID uuid.UUID) error {
	return r.q.AddGarageServiceCategory(ctx, garageID, categoryID)
}

func (r *garageRepository) ListPending(ctx context.Context) ([]models.PendingGarage, error) {
	rows, err := r.q.ListPendingGarages(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.PendingGarage, 0, len(rows))
	for _, row := range rows {
		out = append(out, models.PendingGarage{
			ID: row.ID, OwnerID: row.OwnerID, Name: row.Name,
			LicenseNumber: row.LicenseNumber, Address: row.Address, SubmittedAt: row.SubmittedAt,
		})
	}
	return out, nil
}

func (r *garageRepository) SetVerificationStatus(ctx context.Context, id, reviewerID uuid.UUID, status string) error {
	return r.q.SetGarageVerificationStatus(ctx, id, status, reviewerID)
}

func (r *garageRepository) Deactivate(ctx context.Context, id uuid.UUID) error {
	return r.q.DeactivateGarage(ctx, id)
}

func (r *garageRepository) DeleteMechanic(ctx context.Context, id uuid.UUID) error {
	return r.q.DeleteMechanicByID(ctx, id)
}
