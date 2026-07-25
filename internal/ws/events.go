// Package ws implements the real-time layer: an in-memory connection
// manager plus the event types pushed to clients over WebSocket.
//
// SCALING NOTE: this manager only works because it's the single source
// of truth for "who is connected" within one process. The moment you run
// more than one backend instance (e.g. behind a load balancer), a client
// connected to instance A won't see an event triggered by a request
// handled on instance B. At that point, replace the direct
// manager.SendToUser(...) calls throughout this codebase with a publish
// to a shared channel (Redis Pub/Sub is the natural fit — each instance
// subscribes to a channel per user or a single fan-out channel and
// forwards to its own local connections). The event *shapes* below
// wouldn't need to change at all, only how they get from "something
// happened" to "delivered to a local connection".
package ws

import "time"

type EventType string

const (
	EventNewRequestMatch EventType = "new_request_match" // -> nearby garages, when a car owner creates a request
	EventOfferReceived   EventType = "offer_received"    // -> car owner, when a garage submits an offer
	EventRequestAccepted EventType = "request_accepted"  // -> garage/mechanic, when their offer is accepted
	EventStatusUpdate    EventType = "status_update"     // -> car owner, mechanic location/status changes during a job
	EventJobCompleted    EventType = "job_completed"     // -> car owner + garage, when a booking is marked completed

	// Commission/settlement events (migration 0013). The platform never
	// holds funds, so these announce record-keeping moves, not transfers.
	EventConfirmationRequested EventType = "confirmation_requested" // -> car owner, provider recorded a service they must confirm
	EventCommissionBooked      EventType = "commission_booked"      // -> provider, car owner confirmed; commission now owed
	EventSettlementVerified    EventType = "settlement_verified"    // -> provider, admin verified their settlement payment
)

// Event is the single envelope every WebSocket message uses, so clients
// only need one parser: switch on Type, decode Payload into the shape
// they expect for that type.
type Event struct {
	Type      EventType   `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

func NewEvent(t EventType, payload interface{}) Event {
	return Event{Type: t, Payload: payload, Timestamp: time.Now()}
}

// --- Payload shapes ----------------------------------------------------

type NewRequestMatchPayload struct {
	ServiceRequestID string  `json:"service_request_id"`
	CategoryID       string  `json:"category_id"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	DistanceKM       float64 `json:"distance_km"`
	Description      string  `json:"description,omitempty"`
}

type OfferReceivedPayload struct {
	ServiceRequestID string `json:"service_request_id"`
	OfferID          string `json:"offer_id"`
	GarageID         string `json:"garage_id"`
	Price            string `json:"price"`
	EtaMinutes       *int32 `json:"eta_minutes,omitempty"`
}

type RequestAcceptedPayload struct {
	ServiceRequestID string `json:"service_request_id"`
	OfferID          string `json:"offer_id"`
	BookingID        string `json:"booking_id"`
}

type StatusUpdatePayload struct {
	ServiceRequestID string   `json:"service_request_id"`
	BookingID        string   `json:"booking_id"`
	Status           string   `json:"status,omitempty"`
	MechanicLat      *float64 `json:"mechanic_lat,omitempty"`
	MechanicLng      *float64 `json:"mechanic_lng,omitempty"`
}

type JobCompletedPayload struct {
	ServiceRequestID string `json:"service_request_id"`
	BookingID        string `json:"booking_id"`
}

type ConfirmationRequestedPayload struct {
	TransactionID string  `json:"transaction_id"`
	ServiceName   string  `json:"service_name"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency,omitempty"`
}

type CommissionBookedPayload struct {
	TransactionID string  `json:"transaction_id"`
	GrossAmount   float64 `json:"gross_amount"`
	Commission    float64 `json:"commission"`
}

type SettlementVerifiedPayload struct {
	SettlementID string  `json:"settlement_id"`
	Amount       float64 `json:"amount"`
}
