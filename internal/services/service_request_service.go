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
const matchRadiusKM = 15.0

type ServiceRequestService struct {
	repo    repository.ServiceRequestRepository
	garages repository.GarageRepository
	hub     *ws.Manager
	push    *PushService
	log     zerolog.Logger
}

func NewServiceRequestService(repo repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, push *PushService, log zerolog.Logger) *ServiceRequestService {
	return &ServiceRequestService{repo: repo, garages: garages, hub: hub, push: push, log: log}
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

		// Push reaches the garage owner even if their app is fully
		// closed — the WebSocket event above only reaches an open
		// connection, so both are needed for "even when backgrounded or
		// terminated" (this request's Step 8 requirement).
		s.push.Notify(ctx, g.OwnerID, "New service request nearby", in.Description, map[string]string{
			"service_request_id": id.String(),
			"type":               string(ws.EventNewRequestMatch),
		})
	}

	return id, status, nil
}

func (s *ServiceRequestService) Get(ctx context.Context, id uuid.UUID) (models.ServiceRequest, error) {
	return s.repo.Get(ctx, id)
}

func (s *ServiceRequestService) ListMine(ctx context.Context, ownerID uuid.UUID) ([]models.ServiceRequest, error) {
	return s.repo.ListByOwner(ctx, ownerID, 50)
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
