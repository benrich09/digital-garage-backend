package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/db/sqlcgen"
	"github.com/yourorg/digital-garage/internal/models"
)

type ServiceRequestRepository interface {
	Create(ctx context.Context, ownerID uuid.UUID, in models.CreateServiceRequestInput) (uuid.UUID, string, error)
	Get(ctx context.Context, id uuid.UUID) (models.ServiceRequest, error)
	ListByOwner(ctx context.Context, ownerID uuid.UUID, limit int32) ([]models.ServiceRequest, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string) error
}

type serviceRequestRepository struct {
	q *sqlcgen.Queries
}

func NewServiceRequestRepository(q *sqlcgen.Queries) ServiceRequestRepository {
	return &serviceRequestRepository{q: q}
}

func (r *serviceRequestRepository) Create(ctx context.Context, ownerID uuid.UUID, in models.CreateServiceRequestInput) (uuid.UUID, string, error) {
	desc := in.Description
	photos := in.PhotoURLs
	if photos == nil {
		photos = []string{}
	}
	photoJSON, err := json.Marshal(photos)
	if err != nil {
		return uuid.Nil, "", err
	}

	row, err := r.q.CreateServiceRequest(ctx, sqlcgen.CreateServiceRequestParams{
		CarOwnerID:  ownerID,
		VehicleID:   in.VehicleID,
		CategoryID:  in.CategoryID,
		Description: &desc,
		Lat:         in.Latitude,
		Lng:         in.Longitude,
		PhotoURLs:   photoJSON,
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return row.ID, row.Status, nil
}

func (r *serviceRequestRepository) Get(ctx context.Context, id uuid.UUID) (models.ServiceRequest, error) {
	row, err := r.q.GetServiceRequest(ctx, id)
	if err != nil {
		return models.ServiceRequest{}, err
	}
	var photos []string
	if len(row.PhotoURLs) > 0 {
		_ = json.Unmarshal(row.PhotoURLs, &photos) // malformed jsonb should never happen; ignore rather than fail the read
	}
	return models.ServiceRequest{
		ID:          row.ID,
		CarOwnerID:  row.CarOwnerID,
		VehicleID:   row.VehicleID,
		CategoryID:  row.CategoryID,
		Description: row.Description,
		Status:      row.Status,
		PhotoURLs:   photos,
		Latitude:    row.Latitude,
		Longitude:   row.Longitude,
		RequestedAt: row.RequestedAt,
		ScheduledAt: row.ScheduledAt,
	}, nil
}

func (r *serviceRequestRepository) ListByOwner(ctx context.Context, ownerID uuid.UUID, limit int32) ([]models.ServiceRequest, error) {
	rows, err := r.q.ListServiceRequestsByOwner(ctx, sqlcgen.ListServiceRequestsByOwnerParams{
		CarOwnerID: ownerID,
		MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]models.ServiceRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, models.ServiceRequest{
			ID:          row.ID,
			VehicleID:   row.VehicleID,
			CategoryID:  row.CategoryID,
			Description: row.Description,
			Status:      row.Status,
			RequestedAt: row.RequestedAt,
			ScheduledAt: row.ScheduledAt,
		})
	}
	return out, nil
}

func (r *serviceRequestRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	return r.q.UpdateServiceRequestStatus(ctx, id, status)
}
