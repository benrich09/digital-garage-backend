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

	// Best-effort: persist request_kind + preferred garage if columns exist.
	if s.pool != nil {
		_, _ = s.pool.Exec(ctx, `
			update service_requests
			set request_kind = $2
			where id = $1
		`, id, in.RequestKind)
		if in.PreferredGarageID != nil {
			_, _ = s.pool.Exec(ctx, `
				update service_requests
				set preferred_garage_id = $2
				where id = $1
			`, id, *in.PreferredGarageID)
		}
		if in.ScheduledAt != nil && *in.ScheduledAt != "" {
			_, _ = s.pool.Exec(ctx, `
				update service_requests
				set scheduled_at = $2::timestamptz
				where id = $1
			`, id, *in.ScheduledAt)
		}
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

	// Geo matches (best-effort). Garage booking → only the chosen garage.
	if kind == "garage_booking" {
		targeted := false
		if s.pool != nil {
			var ownerID string
			if err := s.pool.QueryRow(ctx, `
				select g.owner_id::text
				from service_requests sr
				join garages g on g.id = sr.preferred_garage_id
				where sr.id = $1 and sr.preferred_garage_id is not null
			`, id).Scan(&ownerID); err == nil && ownerID != "" {
				notify(ownerID, 0)
				targeted = true
				s.log.Info().Str("owner", ownerID).Str("request_id", id.String()).Msg("booking targeted to preferred garage")
			}
		}
		// Only fan-out to other garages when no preferred garage was set.
		if !targeted && s.garages != nil {
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
		// 3) garages.owner_id — only if nobody was notified yet (no preferred garage)
		if kind == "garage_booking" && len(notified) == 0 {
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
	// Prefer geo query when PostGIS works.
	rows, err := s.repo.ListOpenNear(ctx, lat, lng, geo.KMToMeters(radiusKM), limit)
	if err == nil && len(rows) > 0 {
		return rows, nil
	}
	if err != nil {
		s.log.Warn().Err(err).Msg("ListOpenNear failed — falling back to pending list")
	}
	// Fallback: all pending requests (no geo required). Apps filter by kind/role.
	if s.pool == nil {
		return rows, err
	}
	qrows, qerr := s.pool.Query(ctx, `
		select
			sr.id,
			sr.description,
			sr.status,
			coalesce(sr.request_kind,
				case when sr.description like '%[kind:garage_booking]%' then 'garage_booking'
				     else 'mechanic_request' end),
			coalesce(sr.category_id, '00000000-0000-0000-0000-000000000000'::uuid),
			coalesce(sc.name, ''),
			coalesce(ST_Y(sr.pickup_location::geometry), 0),
			coalesce(ST_X(sr.pickup_location::geometry), 0),
			0::float8 as distance_km,
			coalesce(sr.requested_at, sr.created_at, now()),
			sr.car_owner_id,
			coalesce(p.full_name, ''),
			coalesce(p.phone, ''),
			coalesce(p.avatar_url, ''),
			coalesce(sr.vehicle_id, '00000000-0000-0000-0000-000000000000'::uuid),
			coalesce(v.make, ''),
			coalesce(v.model, ''),
			v.year,
			coalesce(v.plate_number, v.license_plate, '')
		from service_requests sr
		left join service_categories sc on sc.id = sr.category_id
		left join profiles p on p.id = sr.car_owner_id
		left join vehicles v on v.id = sr.vehicle_id
		where lower(coalesce(sr.status,'')) in ('pending','quoted','open')
		order by coalesce(sr.requested_at, sr.created_at) desc
		limit $1
	`, limit)
	if qerr != nil {
		// Minimal columns if schema differs
		qrows2, qerr2 := s.pool.Query(ctx, `
			select id, description, status,
			       coalesce(request_kind, 'mechanic_request'),
			       coalesce(ST_Y(pickup_location::geometry),0),
			       coalesce(ST_X(pickup_location::geometry),0),
			       car_owner_id
			from service_requests
			where lower(coalesce(status,'')) in ('pending','quoted','open')
			order by created_at desc
			limit $1
		`, limit)
		if qerr2 != nil {
			s.log.Warn().Err(qerr).Err(qerr2).Msg("pending fallback query failed")
			return rows, err
		}
		defer qrows2.Close()
		out := make([]models.OpenServiceRequest, 0)
		for qrows2.Next() {
			var it models.OpenServiceRequest
			var desc *string
			var year *int32
			_ = year
			if scanErr := qrows2.Scan(&it.ID, &desc, &it.Status, &it.RequestKind, &it.Latitude, &it.Longitude, &it.OwnerID); scanErr != nil {
				continue
			}
			it.Description = desc
			out = append(out, it)
		}
		return out, nil
	}
	defer qrows.Close()
	out := make([]models.OpenServiceRequest, 0)
	for qrows.Next() {
		var it models.OpenServiceRequest
		var desc *string
		var catName, ownerName, ownerPhone, ownerAvatar, vMake, vModel, vPlate string
		var year *int32
		if scanErr := qrows.Scan(
			&it.ID, &desc, &it.Status, &it.RequestKind, &it.CategoryID, &catName,
			&it.Latitude, &it.Longitude, &it.DistanceKM, &it.RequestedAt, &it.OwnerID,
			&ownerName, &ownerPhone, &ownerAvatar, &it.VehicleID, &vMake, &vModel, &year, &vPlate,
		); scanErr != nil {
			s.log.Warn().Err(scanErr).Msg("scan open request row")
			continue
		}
		it.Description = desc
		if catName != "" {
			it.CategoryName = &catName
		}
		if ownerName != "" {
			it.OwnerName = &ownerName
		}
		if ownerPhone != "" {
			it.OwnerPhone = &ownerPhone
		}
		if ownerAvatar != "" {
			it.OwnerAvatarURL = &ownerAvatar
		}
		if vMake != "" {
			it.VehicleMake = &vMake
		}
		if vModel != "" {
			it.VehicleModel = &vModel
		}
		it.VehicleYear = year
		if vPlate != "" {
			it.VehiclePlate = &vPlate
		}
		out = append(out, it)
	}
	return out, nil
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


// ListPendingSimple returns pending requests with minimal columns (no geo).
func (s *ServiceRequestService) ListPendingSimple(ctx context.Context, limit int32) ([]models.OpenServiceRequest, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("no pool")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		select
			id,
			description,
			status,
			coalesce(request_kind,
				case when description like '%[kind:garage_booking]%' then 'garage_booking'
				     else 'mechanic_request' end),
			coalesce(ST_Y(pickup_location::geometry), 0),
			coalesce(ST_X(pickup_location::geometry), 0),
			car_owner_id
		from service_requests
		where lower(coalesce(status,'')) in ('pending','quoted','open')
		order by coalesce(requested_at, created_at) desc nulls last
		limit $1
	`, limit)
	if err != nil {
		// even simpler
		rows, err = s.pool.Query(ctx, `
			select id, description, status, 'mechanic_request', 0::float8, 0::float8, car_owner_id
			from service_requests
			where lower(coalesce(status,'')) = 'pending'
			order by created_at desc
			limit $1
		`, limit)
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()
	out := make([]models.OpenServiceRequest, 0)
	for rows.Next() {
		var it models.OpenServiceRequest
		var desc *string
		if err := rows.Scan(&it.ID, &desc, &it.Status, &it.RequestKind, &it.Latitude, &it.Longitude, &it.OwnerID); err != nil {
			continue
		}
		it.Description = desc
		out = append(out, it)
	}
	return out, nil
}

// ListPendingForGarageOwner returns garage_booking rows aimed at this owner's garages.
func (s *ServiceRequestService) ListPendingForGarageOwner(ctx context.Context, ownerID string, limit int32) ([]models.OpenServiceRequest, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("no pool")
	}
	rows, err := s.pool.Query(ctx, `
		select
			sr.id,
			sr.description,
			sr.status,
			coalesce(sr.request_kind, 'garage_booking'),
			coalesce(ST_Y(sr.pickup_location::geometry), 0),
			coalesce(ST_X(sr.pickup_location::geometry), 0),
			sr.car_owner_id
		from service_requests sr
		join garages g on g.id = sr.preferred_garage_id
		where g.owner_id::text = $1
		  and lower(coalesce(sr.status,'')) in ('pending','quoted','open')
		order by coalesce(sr.requested_at, sr.created_at) desc nulls last
		limit $2
	`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.OpenServiceRequest, 0)
	for rows.Next() {
		var it models.OpenServiceRequest
		var desc *string
		if err := rows.Scan(&it.ID, &desc, &it.Status, &it.RequestKind, &it.Latitude, &it.Longitude, &it.OwnerID); err != nil {
			continue
		}
		it.Description = desc
		if it.RequestKind == "" {
			it.RequestKind = "garage_booking"
		}
		out = append(out, it)
	}
	return out, nil
}
