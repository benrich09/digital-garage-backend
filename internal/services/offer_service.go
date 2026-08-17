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
		s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventOfferReceived, ws.OfferReceivedPayload{
			ServiceRequestID: in.ServiceRequestID.String(),
			OfferID:          id.String(),
			GarageID:         in.GarageID.String(),
			Price:            in.Price,
			EtaMinutes:       in.EtaMinutes,
		}))
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
func (s *OfferService) ProviderApprove(ctx context.Context, in models.CreateOfferInput, callerID uuid.UUID) (models.AcceptOfferResult, error) {
	if in.Price == "" {
		in.Price = "0"
	}
	if in.Currency == "" {
		in.Currency = "TZS"
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
		s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventRequestAccepted, ws.RequestAcceptedPayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			OfferID:          result.OfferID.String(),
			BookingID:        result.BookingID.String(),
		}))
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

	garage, err := s.garages.GetByID(ctx, result.GarageID)
	if err == nil {
		s.hub.SendToUser(garage.OwnerID.String(), ws.NewEvent(ws.EventRequestAccepted, ws.RequestAcceptedPayload{
			ServiceRequestID: result.ServiceRequestID.String(),
			OfferID:          result.OfferID.String(),
			BookingID:        result.BookingID.String(),
		}))
	} else {
		s.log.Warn().Err(err).Msg("could not load garage to notify of accepted offer")
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
