package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/pkg/geo"
)

type GarageService struct {
	repo repository.GarageRepository
}

func NewGarageService(repo repository.GarageRepository) *GarageService {
	return &GarageService{repo: repo}
}

// FindNearby returns approved, active garages within radiusKM of (lat,
// lng), closest first, capped at limit results.
func (s *GarageService) FindNearby(ctx context.Context, lat, lng, radiusKM float64, limit int32) ([]models.Garage, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	garages, err := s.repo.ListNearby(ctx, lat, lng, geo.KMToMeters(radiusKM), limit)
	if err != nil {
		return nil, fmt.Errorf("find nearby garages: %w", err)
	}
	return garages, nil
}

// SubmitVerification is the second half of garage_owner signup: the
// account already exists (created via Supabase Auth, role=garage_owner
// from the trigger), and this call attaches the business details that
// keep the garage invisible to car owners (verification_status=pending)
// until an admin approves it.
func (s *GarageService) SubmitVerification(ctx context.Context, ownerID uuid.UUID, in models.GarageVerificationInput) (uuid.UUID, error) {
	garageID, err := s.repo.Create(ctx, ownerID, in)
	if err != nil {
		return uuid.Nil, fmt.Errorf("submit garage verification: %w", err)
	}
	for _, catIDStr := range in.CategoryIDs {
		catID, err := uuid.Parse(catIDStr)
		if err != nil {
			continue // skip malformed ids rather than failing the whole submission
		}
		if err := s.repo.AddServiceCategory(ctx, garageID, catID); err != nil {
			return garageID, fmt.Errorf("attach category %s: %w", catIDStr, err)
		}
	}
	return garageID, nil
}

func (s *GarageService) ListPending(ctx context.Context) ([]models.PendingGarage, error) {
	return s.repo.ListPending(ctx)
}

func (s *GarageService) Approve(ctx context.Context, garageID, adminID uuid.UUID) error {
	return s.repo.SetVerificationStatus(ctx, garageID, adminID, "approved")
}

func (s *GarageService) Reject(ctx context.Context, garageID, adminID uuid.UUID) error {
	return s.repo.SetVerificationStatus(ctx, garageID, adminID, "rejected")
}
