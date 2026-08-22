package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/geo"
)

var validTransitions = map[string][]string{
	"pending":     {"quoted", "cancelled", "expired", "accepted"},
	"quoted":      {"accepted", "cancelled", "expired"},
	"accepted":    {"in_progress", "cancelled", "disputed"},
	"in_progress": {"completed", "disputed"},
	"completed":   {"paid", "disputed"},
	"paid":        {"closed", "disputed"},
}

const matchRadiusKM = 500.0

type ServiceRequestService struct {
	repo    repository.ServiceRequestRepository
	garages repository.GarageRepository
	hub     *ws.Manager
	pool    *pgxpool.Pool
	log     zerolog.Logger
}

func NewServiceRequestService(repo repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, pool *pgxpool.Pool, log zerolog.Logger) *ServiceRequestService {
	return &ServiceRequestService{repo: repo, garages: garages, hub: hub, pool: pool, log: log}
}

func containsKindTag(s string) bool {
	return strings.Contains(s, "[kind:")
}

func (s *ServiceRequestService) Create(ctx context.Context, ownerID uuid.UUID, in models.CreateServiceRequestInput) (uuid.UUID, string, error) {
	// Resolve category when the client sent empty / "not listed".
	if in.CategoryID == uuid.Nil && s.pool != nil {
		var catID uuid.UUID
		err := s.pool.QueryRow(ctx, `
			select id from service_categories
			where coalesce(is_active, true) = true
			order by name asc
			limit 1
		`).Scan(&catID)
		if err != nil || catID == uuid.Nil {
			return uuid.Nil, "", fmt.Errorf("no service categories configured — add at least one row to service_categories")
		}
		in.CategoryID = catID
	}
	// Vehicle is optional — zero UUID is fine (nullable FK).
	if s.pool != nil && in.VehicleID != uuid.Nil {
		var exists bool
		_ = s.pool.QueryRow(ctx, `select exists(select 1 from vehicles where id = $1)`, in.VehicleID).Scan(&exists)
		if !exists {
			// Don't fail the whole request — clear vehicle so insert succeeds.
			s.log.Warn().Str("vehicle_id", in.VehicleID.String()).Msg("vehicle not found; creating request without vehicle")
			in.VehicleID = uuid.Nil
		}
	}
	if in.RequestKind == "" {
		in.RequestKind = "mechanic_request"
	}
	// Normalize kind aliases from clients.
	switch strings.ToLower(strings.TrimSpace(in.RequestKind)) {
	case "booking", "garage", "garage_booking", "garage-booking":
		in.RequestKind = "garage_booking"
	case "mechanic", "roadside", "mechanic_request", "mechanic-request", "on_road":
		in.RequestKind = "mechanic_request"
	}

	id, status, err := s.repo.Create(ctx, ownerID, in)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("create service request: %w", err)
	}
	s.log.Info().
		Str("request_id", id.String()).
		Str("kind", in.RequestKind).
		Msg("service request created")

	// Best-effort: persist request_kind column if the migration exists.
	if s.pool != nil {
		_, _ = s.pool.Exec(ctx, `
			update service_requests
			set request_kind = $2
			where id = $1
		`, id, in.RequestKind)
	}

	kind := in.RequestKind
	taggedDesc := in.Description
	if taggedDesc == "" {
		taggedDesc = "[kind:" + kind + "]"
	} else if !containsKindTag(taggedDesc) {
		taggedDesc = "[kind:" + kind + "]\n" + taggedDesc
	}

	// Notify every relevant provider. Geo is best-effort; role broadcast is required.
	s.broadcastNewRequest(ctx, id, in.CategoryID, in.Latitude, in.Longitude, taggedDesc, kind)

	return id, status, nil
}

// broadcastNewRequest pushes WS events to mechanics (for mechanic_request)
// or garage owners (for garage_booking). Always includes a role-wide fallback
// so empty geo tables / missing current_location never hide jobs.
func (s *ServiceRequestService) broadcastNewRequest(
	ctx context.Context,
	id, categoryID uuid.UUID,
	lat, lng float64,
	desc, kind string,
) {
	if s.hub == nil {
		s.log.Warn().Msg("ws hub nil — cannot notify providers")
		return
	}

	notified := map[string]struct{}{}
	notify := func(pid string, distKM float64) {
		if pid == "" {
			return
		}
		if _, ok := notified[pid]; ok {
			return
		}
		notified[pid] = struct{}{}
		s.hub.SendToUser(pid, ws.NewEvent(ws.EventNewRequestMatch, ws.NewRequestMatchPayload{
			ServiceRequestID: id.String(),
			CategoryID:       categoryID.String(),
			Latitude:         lat,
			Longitude:        lng,
			DistanceKM:       distKM,
			Description:      desc,
			RequestKind:      kind,
		}))
	}

	// Geo matches (best-effort).
	if kind == "garage_booking" && s.garages != nil {
		nearby, err := s.garages.ListNearbyOfferingCategory(ctx, lat, lng, geo.KMToMeters(matchRadiusKM), categoryID, 50)
		if err != nil {
			s.log.Warn().Err(err).Msg("nearby garage matching failed")
		} else {
			for _, g := range nearby {
				dist := 0.0
				if g.DistanceKM != nil {
					dist = *g.DistanceKM
				}
				notify(g.OwnerID.String(), dist)
			}
		}
	}
	if kind == "mechanic_request" {
		mechanics, mErr := s.repo.ListNearbyMechanics(ctx, lat, lng, geo.KMToMeters(matchRadiusKM), 50)
		if mErr != nil {
			s.log.Warn().Err(mErr).Msg("nearby mechanic matching failed")
		} else {
			for _, m := range mechanics {
				notify(m.ProfileID.String(), m.DistanceMeters/1000.0)
			}
		}
	}

	// Role-wide fallback — always runs so inbox fills even with no GPS.
	if s.pool != nil {
		roleFilter := "mechanic"
		if kind == "garage_booking" {
			roleFilter = "garage_owner"
		}
		// 1) profiles.role
		rows, err := s.pool.Query(ctx, `
			select id::text from profiles
			where lower(replace(coalesce(role,''), ' ', '_')) = $1
			  and coalesce(is_active, true) = true
			limit 200
		`, roleFilter)
		if err != nil {
			s.log.Warn().Err(err).Msg("broadcast fallback: profiles query failed")
		} else {
			for rows.Next() {
				var pid string
				if rows.Scan(&pid) == nil {
					notify(pid, 0)
				}
			}
			rows.Close()
		}
		// 2) mechanics table → profile_id (covers role typos)
		if kind == "mechanic_request" {
			mrows, merr := s.pool.Query(ctx, `
				select distinct profile_id::text from mechanics
				where profile_id is not null
				limit 200
			`)
			if merr == nil {
				for mrows.Next() {
					var pid string
					if mrows.Scan(&pid) == nil {
						notify(pid, 0)
					}
				}
				mrows.Close()
			}
		}
		// 3) garages.owner_id
		if kind == "garage_booking" {
			grows, gerr := s.pool.Query(ctx, `
				select distinct owner_id::text from garages
				where owner_id is not null
				limit 200
			`)
			if gerr == nil {
				for grows.Next() {
					var pid string
					if grows.Scan(&pid) == nil {
						notify(pid, 0)
					}
				}
				grows.Close()
			}
		}
	}

	s.log.Info().
		Int("notified", len(notified)).
		Str("kind", kind).
		Str("request_id", id.String()).
		Msg("broadcast new request to providers")
}

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

func (s *ServiceRequestService) BrowseOpen(ctx context.Context, lat, lng, radiusKM float64, limit int32) ([]models.OpenServiceRequest, error) {
	if radiusKM <= 0 {
		radiusKM = matchRadiusKM
	}
	if limit <= 0 {
		limit = 50
	}
	return s.repo.ListOpenNear(ctx, lat, lng, geo.KMToMeters(radiusKM), limit)
}

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
