package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/geo"
)

type OfferService struct {
	offers   repository.OfferRepository
	requests repository.ServiceRequestRepository
	garages  repository.GarageRepository
	pool     *pgxpool.Pool
	hub      *ws.Manager
	push     *PushService
	log      zerolog.Logger
}

func NewOfferService(offers repository.OfferRepository, requests repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, log zerolog.Logger) *OfferService {
	return &OfferService{offers: offers, requests: requests, garages: garages, hub: hub, log: log}
}

// WithPool attaches the DB pool so Decline can record silent declines
// without a full sqlc regenerate.
func (s *OfferService) WithPool(pool *pgxpool.Pool) *OfferService {
	s.pool = pool
	return s
}

func (s *OfferService) WithPush(p *PushService) *OfferService {
	s.push = p
	return s
}

// notifyCarOwner delivers WS + optional FCM so the customer app sees
// booking / offer updates even when the socket is offline.
func (s *OfferService) notifyCarOwner(ctx context.Context, carOwnerID uuid.UUID, evt ws.Event, title, body string, data map[string]string) {
	if s.hub != nil && carOwnerID != uuid.Nil {
		s.hub.SendToUser(carOwnerID.String(), evt)
	}
	if s.push != nil && carOwnerID != uuid.Nil && title != "" {
		s.push.Notify(ctx, carOwnerID, title, body, data)
	}
}

// Create is called by a garage_owner (or a mechanic acting for their
// garage) submitting a quote against an open request. Notifies the car
// owner in real time via offer_received.
func (s *OfferService) Create(ctx context.Context, in models.CreateOfferInput) (uuid.UUID, string, error) {
	id, status, err := s.offers.Create(ctx, in)
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("create offer: %w", err)
	}

	req, err := s.requests.Get(ctx, in.ServiceRequestID)
	if err == nil {
		s.notifyCarOwner(ctx, req.CarOwnerID, ws.NewEvent(ws.EventOfferReceived, ws.OfferReceivedPayload{
			ServiceRequestID: in.ServiceRequestID.String(),
			OfferID:          id.String(),
			GarageID:         in.GarageID.String(),
			Price:            in.Price,
			EtaMinutes:       in.EtaMinutes,
		}), "New offer", "A provider sent you a quote. Open the app to review it.", map[string]string{
			"type":               "offer_received",
			"service_request_id": in.ServiceRequestID.String(),
			"offer_id":           id.String(),
		})
	} else {
		s.log.Warn().Err(err).Msg("could not load request to notify car owner of new offer")
	}

	return id, status, nil
}

func (s *OfferService) ListForRequest(ctx context.Context, requestID uuid.UUID) ([]models.Offer, error) {
	return s.offers.ListForRequest(ctx, requestID)
}

// ProviderApprove creates an offer from the garage/mechanic and immediately
// accepts it (booking created). Used for Approve/Deny UX.
//
// Always attaches the caller's mechanic row (so active-jobs RLS + UI keep
// the job visible) and fills price from provider_services when the client
// sent 0 / empty — the agreed service rate, not a post-job amount.
func (s *OfferService) ProviderApprove(ctx context.Context, in models.CreateOfferInput, callerID uuid.UUID) (models.AcceptOfferResult, error) {
	if s.pool != nil {
		// Resolve mechanic + garage for this caller so the booking is linked.
		var mechID, garageID uuid.UUID
		err := s.pool.QueryRow(ctx, `
			select m.id, m.garage_id from mechanics m
			where m.profile_id = $1
			limit 1
		`, callerID).Scan(&mechID, &garageID)
		if err == nil {
			if in.MechanicID == nil {
				in.MechanicID = &mechID
			}
			if in.GarageID == uuid.Nil {
				in.GarageID = garageID
			}
		}
		if in.GarageID == uuid.Nil {
			_ = s.pool.QueryRow(ctx, `
				select id from garages where owner_id = $1 order by created_at asc limit 1
			`, callerID).Scan(&in.GarageID)
		}
		// Prefer pre-listed service price when client sent 0 / empty.
		if in.Price == "" || in.Price == "0" {
			var price string
			// Prefer a service marked for the request kind; otherwise any active price.
			err := s.pool.QueryRow(ctx, `
				select coalesce(ps.price::text, '0')
				from provider_services ps
				where ps.provider_id = $1 and ps.is_active = true
				order by ps.is_roadside desc, ps.created_at desc
				limit 1
			`, callerID).Scan(&price)
			if err == nil && price != "" && price != "0" {
				in.Price = price
			}
		}
	}
	if in.Price == "" {
		in.Price = "0"
	}
	if in.Currency == "" {
		in.Currency = "TZS"
	}
	// Independent mechanics still need a garage_id (NOT NULL on offers).
	// Auto-create a personal "Mobile mechanic" garage so approve never
	// dies with "garage_id is required".
	if in.GarageID == uuid.Nil {
		var gid uuid.UUID
		err := s.pool.QueryRow(ctx, `
			insert into garages (
			  owner_id, name, description, address, location,
			  is_active, verification_status, is_verified
			) values (
			  $1,
			  'Mobile mechanic',
			  'Personal mechanic profile (auto-created)',
			  'On-site',
			  ST_SetSRID(ST_MakePoint(39.2083, -6.7924), 4326)::geography,
			  true,
			  'approved',
			  true
			)
			returning id
		`, callerID).Scan(&gid)
		if err != nil {
			// Maybe one already exists from a race — re-read
			_ = s.pool.QueryRow(ctx, `
				select id from garages where owner_id = $1 order by created_at asc limit 1
			`, callerID).Scan(&gid)
		}
		if gid == uuid.Nil {
			return models.AcceptOfferResult{}, fmt.Errorf("could not resolve garage for provider — create a garage profile in the app")
		}
		in.GarageID = gid
		// Link mechanic row to this garage if possible
		if in.MechanicID != nil {
			_, _ = s.pool.Exec(ctx, `update mechanics set garage_id = $1 where id = $2`, gid, *in.MechanicID)
		}
	}
	id, _, err := s.offers.Create(ctx, in)
	if err != nil {
		// Duplicate offer for same request+garage → load existing pending and accept
		existing, listErr := s.offers.ListForRequest(ctx, in.ServiceRequestID)
		if listErr == nil {
			for _, o := range existing {
				if o.Status == "pending" && (o.GarageID == in.GarageID || in.GarageID == uuid.Nil) {
					id = o.ID
					err = nil
					break
				}
			}
		}
		if err != nil {
			return models.AcceptOfferResult{}, fmt.Errorf("create offer: %w", err)
		}
	}
	result, err := s.offers.Accept(ctx, id)
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("accept offer: %w", err)
	}

	req, err := s.requests.Get(ctx, in.ServiceRequestID)
	if err == nil {
		kind := ""
		if s.pool != nil {
			_ = s.pool.QueryRow(ctx, `select coalesce(request_kind,'') from service_requests where id = $1`, in.ServiceRequestID).Scan(&kind)
		}
		garageID := ""
		if result.GarageID != uuid.Nil {
			garageID = result.GarageID.String()
		}
		mechID := ""
		if result.MechanicID != nil {
			mechID = result.MechanicID.String()
		}
		// 1) Explicit booking_created so car app can navigate / refresh track screen
		s.notifyCarOwner(ctx, req.CarOwnerID, ws.NewEvent(ws.EventBookingCreated, ws.BookingCreatedPayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			BookingID:        result.BookingID.String(),
			OfferID:          result.OfferID.String(),
			GarageID:         garageID,
			MechanicID:       mechID,
			Status:           "scheduled",
			RequestKind:      kind,
		}), "Booking confirmed", "A provider accepted your request. Track the job in the app.", map[string]string{
			"type":               "booking_created",
			"service_request_id": result.ServiceRequestID.String(),
			"booking_id":         result.BookingID.String(),
			"request_kind":       kind,
		})
		// 2) Status update (existing car-app listeners)
		s.notifyCarOwner(ctx, req.CarOwnerID, ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			BookingID:        result.BookingID.String(),
			Status:           "accepted",
		}), "", "", nil)
		// 3) Legacy request_accepted (some builds listen for this)
		s.notifyCarOwner(ctx, req.CarOwnerID, ws.NewEvent(ws.EventRequestAccepted, ws.RequestAcceptedPayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			OfferID:          result.OfferID.String(),
			BookingID:        result.BookingID.String(),
		}), "", "", nil)
		s.log.Info().
			Str("car_owner", req.CarOwnerID.String()).
			Str("booking_id", result.BookingID.String()).
			Str("request_id", result.ServiceRequestID.String()).
			Msg("notified car owner of new booking")
	}
	_ = callerID
	return result, nil
}

// Accept is called by the car owner. Locks the request (via the
// underlying transaction: other offers rejected, booking created,
// request status -> accepted) and notifies the winning garage (and
// mechanic, if one was named on the offer) in real time.
func (s *OfferService) Accept(ctx context.Context, offerID uuid.UUID, callerID uuid.UUID) (models.AcceptOfferResult, error) {
	offer, err := s.offers.Get(ctx, offerID)
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("load offer: %w", err)
	}
	req, err := s.requests.Get(ctx, offer.ServiceRequestID)
	if err != nil {
		return models.AcceptOfferResult{}, fmt.Errorf("load request: %w", err)
	}
	if req.CarOwnerID != callerID {
		return models.AcceptOfferResult{}, fmt.Errorf("forbidden: not your request")
	}

	result, err := s.offers.Accept(ctx, offerID)
	if err != nil {
		return models.AcceptOfferResult{}, err
	}

	// Confirm booking back to the car owner (other devices / track screen)
	s.notifyCarOwner(ctx, req.CarOwnerID, ws.NewEvent(ws.EventBookingCreated, ws.BookingCreatedPayload{
		ServiceRequestID: result.ServiceRequestID.String(),
		BookingID:        result.BookingID.String(),
		OfferID:          result.OfferID.String(),
		GarageID:         result.GarageID.String(),
		Status:           "scheduled",
	}), "Booking confirmed", "Your booking is confirmed. Track progress in the app.", map[string]string{
		"type":               "booking_created",
		"service_request_id": result.ServiceRequestID.String(),
		"booking_id":         result.BookingID.String(),
	})

	garage, err := s.garages.GetByID(ctx, result.GarageID)
	if err == nil {
		s.hub.SendToUser(garage.OwnerID.String(), ws.NewEvent(ws.EventRequestAccepted, ws.RequestAcceptedPayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			OfferID:          result.OfferID.String(),
			BookingID:        result.BookingID.String(),
		}))
		if s.push != nil {
			s.push.Notify(ctx, garage.OwnerID, "Job accepted", "A car owner accepted your offer.", map[string]string{
				"type":               "request_accepted",
				"service_request_id": result.ServiceRequestID.String(),
				"booking_id":         result.BookingID.String(),
			})
		}
	} else {
		s.log.Warn().Err(err).Msg("could not load garage to notify of accepted offer")
	}
	// Also notify assigned mechanic profile if present
	if result.MechanicID != nil && s.pool != nil {
		var mechProfile uuid.UUID
		if e := s.pool.QueryRow(ctx, `select profile_id from mechanics where id = $1`, *result.MechanicID).Scan(&mechProfile); e == nil && mechProfile != uuid.Nil {
			s.hub.SendToUser(mechProfile.String(), ws.NewEvent(ws.EventRequestAccepted, ws.RequestAcceptedPayload{
				ServiceRequestID: result.ServiceRequestID.String(),
				OfferID:          result.OfferID.String(),
				BookingID:        result.BookingID.String(),
			}))
			if s.push != nil {
				s.push.Notify(ctx, mechProfile, "Job accepted", "You have a new job.", map[string]string{
					"type":               "request_accepted",
					"booking_id":         result.BookingID.String(),
					"service_request_id": result.ServiceRequestID.String(),
				})
			}
		}
	}

	return result, nil
}

// Decline records a silent soft-decline by a provider. The request stays
// "pending" so other mechanics/garages can still accept; the car owner is
// never told that this particular provider said no. After recording the
// decline we best-effort re-broadcast the match to other nearby mechanics
// who have not already declined.
func (s *OfferService) Decline(ctx context.Context, requestID, providerID uuid.UUID, reason *string) error {
	req, err := s.requests.Get(ctx, requestID)
	if err != nil {
		return fmt.Errorf("load request: %w", err)
	}
	// Only decline while still open.
	if req.Status != "pending" && req.Status != "quoted" {
		return fmt.Errorf("request is no longer open (%s)", req.Status)
	}

	if s.pool != nil {
		_, err = s.pool.Exec(ctx, `
			insert into request_declines (service_request_id, provider_id, reason)
			values ($1, $2, $3)
			on conflict (service_request_id, provider_id) do nothing
		`, requestID, providerID, reason)
		if err != nil {
			// Table may not exist yet on older deploys — log and continue so
			// the provider still gets a clean "declined" response.
			s.log.Warn().Err(err).Msg("could not record request_decline (migration 0019 applied?)")
		}
	}

	// Best-effort: fan out again to nearby mechanics who have not declined.
	// Failures here must not surface to the declining provider.
	if s.pool != nil && req.Latitude != 0 && req.Longitude != 0 {
		rows, err := s.pool.Query(ctx, `
			select m.profile_id,
			       ST_Distance(
			         m.current_location,
			         ST_SetSRID(ST_MakePoint($2::float8, $1::float8), 4326)::geography
			       ) as distance_meters
			from mechanics m
			join profiles p on p.id = m.profile_id
			where p.is_active = true
			  and p.role = 'mechanic'
			  and m.is_available = true
			  and m.current_location is not null
			  and m.profile_id <> $3
			  and not exists (
			    select 1 from request_declines rd
			    where rd.service_request_id = $4 and rd.provider_id = m.profile_id
			  )
			  and ST_DWithin(
			    m.current_location,
			    ST_SetSRID(ST_MakePoint($2::float8, $1::float8), 4326)::geography,
			    $5
			  )
			order by distance_meters
			limit 10
		`, req.Latitude, req.Longitude, providerID, requestID, geo.KMToMeters(matchRadiusKM))
		if err != nil {
			s.log.Warn().Err(err).Msg("re-match after decline failed")
		} else {
			defer rows.Close()
			for rows.Next() {
				var pid uuid.UUID
				var dist float64
				if err := rows.Scan(&pid, &dist); err != nil {
					continue
				}
				desc := ""
				if req.Description != nil {
					desc = *req.Description
				}
				s.hub.SendToUser(pid.String(), ws.NewEvent(ws.EventNewRequestMatch, ws.NewRequestMatchPayload{
					ServiceRequestID: requestID.String(),
					CategoryID:       req.CategoryID.String(),
					Latitude:         req.Latitude,
					Longitude:        req.Longitude,
					DistanceKM:       dist / 1000.0,
					Description:      desc,
				}))
			}
		}
	}

	s.log.Info().
		Str("request_id", requestID.String()).
		Str("provider_id", providerID.String()).
		Msg("provider soft-declined request (silent to owner)")
	return nil
}
