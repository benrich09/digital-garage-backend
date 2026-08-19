package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/geo"
)

// validTransitions encodes the state machine from the schema design —
// enforced here in application code since Postgres only stores the enum,
// it doesn't police which transitions are legal.
var validTransitions = map[string][]string{
	"pending":     {"quoted", "cancelled", "expired"},
	"quoted":      {"accepted", "cancelled", "expired"},
	"accepted":    {"in_progress", "cancelled", "disputed"},
	"in_progress": {"completed", "disputed"},
	"completed":   {"paid", "disputed"},
	"paid":        {"closed", "disputed"},
}

// matchRadiusKM is the default radius used to find nearby garages when
// a new request comes in. Not exposed as user input (yet) — a natural
// next step is making this configurable per category or per city.
const matchRadiusKM = 200.0

type ServiceRequestService struct {
	repo    repository.ServiceRequestRepository
	garages repository.GarageRepository
	hub     *ws.Manager
	log     zerolog.Logger
}

func NewServiceRequestService(repo repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, log zerolog.Logger) *ServiceRequestService {
	return &ServiceRequestService{repo: repo, garages: garages, hub: hub, log: log}
}

func (s *ServiceRequestService) Create(ctx context.Context, ownerID uuid.UUID, in models.CreateServiceRequestInput) (uuid.UUID, string, error) {
	id, status, err := s.repo.Create(ctx, ownerID, in)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("create service request: %w", err)
	}
	s.log.Info().Str("request_id", id.String()).Msg("service request created")

	// Fan out to nearby garages that offer this category. Best-effort:
	// a failure here shouldn't fail the request creation itself, since
	// the request already exists and garages can still find it via the
	// "browse open requests near me" endpoint.
	nearby, err := s.garages.ListNearbyOfferingCategory(ctx, in.Latitude, in.Longitude, geo.KMToMeters(matchRadiusKM), in.CategoryID, 25)
	if err != nil {
		s.log.Warn().Err(err).Msg("nearby garage matching failed, request still created")
		return id, status, nil
	}

	for _, g := range nearby {
		distKM := 0.0
		if g.DistanceKM != nil {
			distKM = *g.DistanceKM
		}
		s.hub.SendToUser(g.OwnerID.String(), ws.NewEvent(ws.EventNewRequestMatch, ws.NewRequestMatchPayload{
			ServiceRequestID: id.String(),
			CategoryID:       in.CategoryID.String(),
			Latitude:         in.Latitude,
			Longitude:        in.Longitude,
			DistanceKM:       distKM,
			Description:      in.Description,
		}))
	}

	// Also fan out to available field mechanics near the request so
	// independent mechanics (not only garage owners) see the job live.
	mechanics, mErr := s.repo.ListNearbyMechanics(ctx, in.Latitude, in.Longitude, geo.KMToMeters(matchRadiusKM), 25)
	if mErr != nil {
		s.log.Warn().Err(mErr).Msg("nearby mechanic matching failed, request still created")
	} else {
		for _, m := range mechanics {
			s.hub.SendToUser(m.ProfileID.String(), ws.NewEvent(ws.EventNewRequestMatch, ws.NewRequestMatchPayload{
				ServiceRequestID: id.String(),
				CategoryID:       in.CategoryID.String(),
				Latitude:         in.Latitude,
				Longitude:        in.Longitude,
				DistanceKM:       m.DistanceMeters / 1000.0,
				Description:      in.Description,
			}))
		}
	}

	return id, status, nil
}

// Cancel lets the car owner withdraw a pending/quoted request.
func (s *ServiceRequestService) Cancel(ctx context.Context, id, ownerID uuid.UUID) error {
	if err := s.repo.Cancel(ctx, id, ownerID); err != nil {
		return fmt.Errorf("cancel service request: %w", err)
	}
	s.log.Info().Str("request_id", id.String()).Msg("service request cancelled by owner")
	return nil
}

func (s *ServiceRequestService) Get(ctx context.Context, id uuid.UUID) (models.ServiceRequest, error) {
	return s.repo.Get(ctx, id)
}

func (s *ServiceRequestService) ListMine(ctx context.Context, ownerID uuid.UUID) ([]models.ServiceRequest, error) {
	return s.repo.ListByOwner(ctx, ownerID, 50)
}

// BrowseOpen returns pending requests near a point (the provider's garage
// or current location). This is the "catch up on work I missed while
// offline" companion to the live new_request_match WebSocket event.
func (s *ServiceRequestService) BrowseOpen(ctx context.Context, lat, lng, radiusKM float64, limit int32) ([]models.OpenServiceRequest, error) {
	if radiusKM <= 0 {
		radiusKM = matchRadiusKM
	}
	if limit <= 0 {
		limit = 25
	}
	return s.repo.ListOpenNear(ctx, lat, lng, geo.KMToMeters(radiusKM), limit)
}

// Transition moves a request to newStatus if, and only if, that move is
// legal from its current status. Everything here is application-level
// validation; the actual row update still goes through RLS in Postgres,
// so a caller without rights to touch the row will still be rejected
// there even if this check passes.
func (s *ServiceRequestService) Transition(ctx context.Context, id uuid.UUID, newStatus string) error {
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("transition: load current state: %w", err)
	}

	allowed := validTransitions[current.Status]
	ok := false
	for _, st := range allowed {
		if st == newStatus {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("illegal transition: %s -> %s", current.Status, newStatus)
	}

	if err := s.repo.UpdateStatus(ctx, id, newStatus); err != nil {
		return fmt.Errorf("transition: update status: %w", err)
	}
	s.log.Info().
		Str("request_id", id.String()).
		Str("from", current.Status).
		Str("to", newStatus).
		Msg("service request transitioned")
	return nil
}
