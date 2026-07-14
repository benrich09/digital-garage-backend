package services

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/yourorg/digital-garage/internal/models"
	"github.com/yourorg/digital-garage/internal/repository"
	"github.com/yourorg/digital-garage/internal/ws"
	"github.com/yourorg/digital-garage/pkg/mpesa"
	"github.com/yourorg/digital-garage/pkg/selcom"
)

type PaymentService struct {
	payments repository.PaymentRepository
	bookings repository.BookingRepository
	offers   repository.OfferRepository
	requests repository.ServiceRequestRepository
	mpesa    *mpesa.Client
	selcom   *selcom.Client
	hub      *ws.Manager
	log      zerolog.Logger
}

func NewPaymentService(
	payments repository.PaymentRepository,
	bookings repository.BookingRepository,
	offers repository.OfferRepository,
	requests repository.ServiceRequestRepository,
	mpesaClient *mpesa.Client,
	selcomClient *selcom.Client,
	hub *ws.Manager,
	log zerolog.Logger,
) *PaymentService {
	return &PaymentService{payments: payments, bookings: bookings, offers: offers, requests: requests, mpesa: mpesaClient, selcom: selcomClient, hub: hub, log: log}
}

// InitiateResult carries back whatever a provider-specific charge call
// returned, in a shape the handler can render without caring which
// provider was used.
type InitiateResult struct {
	Payment         models.Payment
	ProviderStatus  string
	CustomerMessage string
}

// Initiate starts a mobile money charge for a completed booking. Only
// the request's car owner may pay for it, and only once the booking is
// actually completed. in.Provider selects which rail to charge against
// ("mpesa" or "selcom").
func (s *PaymentService) Initiate(ctx context.Context, callerID uuid.UUID, in models.InitiatePaymentInput) (InitiateResult, error) {
	bookingID, err := uuid.Parse(in.BookingID)
	if err != nil {
		return InitiateResult{}, fmt.Errorf("invalid booking_id")
	}

	provider := in.Provider
	if provider == "" {
		provider = "mpesa"
	}
	if provider != "mpesa" && provider != "selcom" {
		return InitiateResult{}, fmt.Errorf("unsupported provider %q: must be 'mpesa' or 'selcom'", provider)
	}

	booking, err := s.bookings.Get(ctx, bookingID)
	if err != nil {
		return InitiateResult{}, fmt.Errorf("load booking: %w", err)
	}
	if booking.Status != "completed" {
		return InitiateResult{}, fmt.Errorf("booking is not completed yet (status=%s)", booking.Status)
	}

	req, err := s.requests.Get(ctx, booking.ServiceRequestID)
	if err != nil {
		return InitiateResult{}, fmt.Errorf("load request: %w", err)
	}
	if req.CarOwnerID != callerID {
		return InitiateResult{}, fmt.Errorf("forbidden: not your booking")
	}

	offer, err := s.offers.Get(ctx, booking.OfferID)
	if err != nil {
		return InitiateResult{}, fmt.Errorf("load offer: %w", err)
	}

	txRef := "dg-" + uuid.NewString()
	payment, err := s.payments.Create(ctx, bookingID, offer.Price, offer.Currency, provider, txRef)
	if err != nil {
		return InitiateResult{}, fmt.Errorf("create payment record: %w", err)
	}

	switch provider {
	case "mpesa":
		if s.mpesa == nil {
			return InitiateResult{Payment: payment}, fmt.Errorf("mpesa is not configured on this server")
		}
		resp, err := s.mpesa.InitiateSTKPush(ctx, mpesa.STKPushRequest{
			PhoneNumber:     in.PhoneNumber,
			Amount:          offer.Price,
			AccountRef:      txRef,
			TransactionDesc: "Digital Garage service payment",
		})
		if err != nil {
			return InitiateResult{Payment: payment}, fmt.Errorf("initiate m-pesa stk push: %w", err)
		}
		// Daraja's callback echoes CheckoutRequestID, not our own AccountReference,
		// so re-key the payment row to that value now for the callback to find later.
		if resp.CheckoutRequestID != "" {
			if err := s.payments.UpdateProviderTxRef(ctx, txRef, resp.CheckoutRequestID); err != nil {
				s.log.Warn().Err(err).Msg("failed to re-key payment to mpesa CheckoutRequestID")
			}
		}
		return InitiateResult{Payment: payment, ProviderStatus: resp.ResponseDescription, CustomerMessage: resp.CustomerMessage}, nil

	case "selcom":
		if s.selcom == nil {
			return InitiateResult{Payment: payment}, fmt.Errorf("selcom is not configured on this server")
		}
		resp, err := s.selcom.InitiateWalletCharge(ctx, selcom.WalletChargeRequest{
			OrderID:     txRef,
			Amount:      offer.Price,
			Currency:    offer.Currency,
			PhoneNumber: in.PhoneNumber,
			BuyerEmail:  callerID.String() + "@digitalgarage.invalid",
			BuyerName:   "Digital Garage customer",
		})
		if err != nil {
			return InitiateResult{Payment: payment}, fmt.Errorf("initiate selcom charge: %w", err)
		}
		// Selcom echoes our own order_id back on its webhook, so no re-keying needed.
		return InitiateResult{Payment: payment, ProviderStatus: resp.Result, CustomerMessage: resp.Message}, nil
	}

	// Unreachable given the validation above, but keeps the compiler happy.
	return InitiateResult{Payment: payment}, fmt.Errorf("unsupported provider")
}

// HandleMpesaCallback processes a Daraja STK push callback, matched by
// CheckoutRequestID (see the re-keying note in Initiate above).
func (s *PaymentService) HandleMpesaCallback(ctx context.Context, payload mpesa.CallbackPayload) error {
	checkoutID := payload.Body.StkCallback.CheckoutRequestID

	ourStatus := "failed"
	if payload.Body.StkCallback.ResultCode == 0 {
		ourStatus = "paid"
	}

	receipt := payload.ReceiptNumber()
	if err := s.payments.MarkSettled(ctx, checkoutID, ourStatus, &receipt, nil); err != nil {
		return fmt.Errorf("mark payment settled: %w", err)
	}
	return s.notifyIfPaid(ctx, checkoutID, ourStatus)
}

// HandleSelcomWebhook processes a Selcom payment-status webhook.
func (s *PaymentService) HandleSelcomWebhook(ctx context.Context, digestHeader string, rawBody []byte, payload selcom.WebhookPayload) error {
	if s.selcom == nil || !s.selcom.VerifyWebhookSignature(digestHeader, rawBody) {
		return fmt.Errorf("invalid webhook signature")
	}

	ourStatus := "failed"
	if payload.PaymentStatus == "COMPLETED" {
		ourStatus = "paid"
	}

	transID := payload.TransID
	if err := s.payments.MarkSettled(ctx, payload.OrderID, ourStatus, &transID, rawBody); err != nil {
		return fmt.Errorf("mark payment settled: %w", err)
	}
	return s.notifyIfPaid(ctx, payload.OrderID, ourStatus)
}

func (s *PaymentService) notifyIfPaid(ctx context.Context, txRef, status string) error {
	if status != "paid" {
		return nil
	}

	payment, err := s.payments.GetByTxRef(ctx, txRef)
	if err != nil {
		s.log.Warn().Err(err).Str("tx_ref", txRef).Msg("paid callback received but payment lookup failed")
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
