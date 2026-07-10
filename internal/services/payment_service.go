package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/flutterwave"
)

type PaymentService struct {
	payments repository.PaymentRepository
	bookings repository.BookingRepository
	offers   repository.OfferRepository
	requests repository.ServiceRequestRepository
	flw      *flutterwave.Client
	hub      *ws.Manager
	log      zerolog.Logger
}

func NewPaymentService(
	payments repository.PaymentRepository,
	bookings repository.BookingRepository,
	offers repository.OfferRepository,
	requests repository.ServiceRequestRepository,
	flw *flutterwave.Client,
	hub *ws.Manager,
	log zerolog.Logger,
) *PaymentService {
	return &PaymentService{payments: payments, bookings: bookings, offers: offers, requests: requests, flw: flw, hub: hub, log: log}
}

// Initiate starts a mobile money charge for a completed booking. Only
// the request's car owner may pay for it, and only once the booking is
// actually completed — paying for a job that hasn't happened yet isn't
// a state this API allows.
func (s *PaymentService) Initiate(ctx context.Context, callerID uuid.UUID, in models.InitiatePaymentInput) (*flutterwave.ChargeResponse, models.Payment, error) {
	bookingID, err := uuid.Parse(in.BookingID)
	if err != nil {
		return nil, models.Payment{}, fmt.Errorf("invalid booking_id")
	}

	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return nil, models.Payment{}, fmt.Errorf("load booking: %w", err)
	}
	if booking.Status != "completed" {
		return nil, models.Payment{}, fmt.Errorf("booking is not completed yet (status=%s)", booking.Status)
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err != nil {
		return nil, models.Payment{}, fmt.Errorf("load request: %w", err)
	}
	if req.CarOwnerID != callerID {
		return nil, models.Payment{}, fmt.Errorf("forbidden: not your booking")
	}

	offer, err := s.offers.Get(ctx, booking.OfferID)
	if err != nil {
		return nil, models.Payment{}, fmt.Errorf("load offer: %w", err)
	}

	txRef := "dg-" + uuid.NewString()
	payment, err := s.payments.Create(ctx, bookingID, offer.Price, offer.Currency, txRef)
	if err != nil {
		return nil, models.Payment{}, fmt.Errorf("create payment record: %w", err)
	}

	chargeResp, err := s.flw.InitiateMobileMoneyCharge(ctx, flutterwave.MobileMoneyChargeRequest{
		TxRef:       txRef,
		Amount:      offer.Price,
		Currency:    offer.Currency,
		Email:       callerID.String() + "@digitalgarage.invalid", // Flutterwave requires an email; car owners sign up via phone/OTP only
		PhoneNumber: in.PhoneNumber,
		Network:     in.Network,
	})
	if err != nil {
		return nil, payment, fmt.Errorf("initiate mobile money charge: %w", err)
	}

	return chargeResp, payment, nil
}

// HandleWebhook processes a Flutterwave webhook callback: verifies the
// static verif-hash secret, matches the tx_ref back to our payment row,
// and — on success — marks the request 'paid' and notifies the car
// owner and garage over WebSocket.
func (s *PaymentService) HandleWebhook(ctx context.Context, verifHashHeader string, rawBody []byte, txRef, providerStatus, providerTxnID string) error {
	if !s.flw.VerifyWebhookHash(verifHashHeader) {
		return fmt.Errorf("invalid webhook signature")
	}

	ourStatus := "failed"
	if providerStatus == "successful" || providerStatus == "completed" {
		ourStatus = "paid"
	}

	txnID := providerTxnID
	if err := s.payments.MarkSettled(ctx, txRef, ourStatus, &txnID, rawBody); err != nil {
		return fmt.Errorf("mark payment settled: %w", err)
	}

	if ourStatus != "paid" {
		return nil
	}

	payment, err := s.payments.GetByTxRef(ctx, txRef)
	if err != nil {
		s.log.Warn().Err(err).Str("tx_ref", txRef).Msg("paid webhook received but payment lookup failed")
		return nil
	}

	booking, err := s.bookings.Get(ctx, payment.BookingID)
	if err != nil {
		return nil
	}
	_ = s.requests.UpdateStatus(ctx, booking.ServiceRequestID, "paid")

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err == nil {
		s.hub.SendToUser(req.CarOwnerID.String(), ws.NewEvent(ws.EventStatusUpdate, ws.StatusUpdatePayload{
			ServiceRequestID: booking.ServiceRequestID.String(),
			BookingID:        booking.ID.String(),
			Status:           "paid",
		}))
	}

	return nil
}
