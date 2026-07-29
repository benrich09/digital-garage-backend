package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
)

type OfferService struct {
	offers   repository.OfferRepository
	requests repository.ServiceRequestRepository
	garages  repository.GarageRepository
	hub      *ws.Manager
	log      zerolog.Logger
}

func NewOfferService(offers repository.OfferRepository, requests repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, log zerolog.Logger) *OfferService {
	return &OfferService{offers: offers, requests: requests, garages: garages, hub: hub, log: log}
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
