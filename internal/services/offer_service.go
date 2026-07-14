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
	push     *PushService
	log      zerolog.Logger
}

func NewOfferService(offers repository.OfferRepository, requests repository.ServiceRequestRepository, garages repository.GarageRepository, hub *ws.Manager, push *PushService, log zerolog.Logger) *OfferService {
	return &OfferService{offers: offers, requests: requests, garages: garages, hub: hub, push: push, log: log}
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
		s.push.Notify(ctx, garage.OwnerID, "Offer accepted!", "A car owner accepted your offer — the job is now booked.", map[string]string{
			"service_request_id": result.ServiceRequestID.String(),
			"booking_id":         result.BookingID.String(),
			"type":               ws.EventRequestAccepted,
		})
	} else {
		s.log.Warn().Err(err).Msg("could not load garage to notify of accepted offer")
	}

	return result, nil
}
