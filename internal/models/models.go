// Package models defines the domain types services and handlers speak
// in — plain structs with JSON tags, independent of whatever sqlc's
// generated row types look like. This indirection is cheap (a handful
// of struct copies per request) and pays for itself by letting the DB
// layer change shape without rippling into HTTP responses.
package models

import (
	"time"

	"github.com/google/uuid"
)

type Garage struct {
	ID         uuid.UUID `json:"id"`
	OwnerID    uuid.UUID `json:"owner_id"`
	Name       string    `json:"name"`
	Address    *string   `json:"address,omitempty"`
	IsVerified bool      `json:"is_verified"`
	Latitude   float64   `json:"latitude"`
	Longitude  float64   `json:"longitude"`
	DistanceKM *float64  `json:"distance_km,omitempty"`
}

type PendingGarage struct {
	ID            uuid.UUID `json:"id"`
	OwnerID       uuid.UUID `json:"owner_id"`
	Name          string    `json:"name"`
	LicenseNumber *string   `json:"license_number,omitempty"`
	Address       *string   `json:"address,omitempty"`
	SubmittedAt   time.Time `json:"submitted_at"`
}

type ServiceRequest struct {
	ID          uuid.UUID  `json:"id"`
	CarOwnerID  uuid.UUID  `json:"car_owner_id"`
	VehicleID   uuid.UUID  `json:"vehicle_id"`
	CategoryID  uuid.UUID  `json:"category_id"`
	Description *string    `json:"description,omitempty"`
	Status      string     `json:"status"`
	PhotoURLs   []string   `json:"photo_urls,omitempty"`
	Latitude    float64    `json:"latitude"`
	Longitude   float64    `json:"longitude"`
	RequestedAt time.Time  `json:"requested_at"`
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
}

// OpenServiceRequest is the lightweight shape a provider sees when
// browsing open (pending) work near them — no car-owner PII, just what's
// needed to decide whether to send an offer, plus the distance.
type OpenServiceRequest struct {
	ID          uuid.UUID `json:"id"`
	Description *string   `json:"description,omitempty"`
	Status      string    `json:"status"`
	Latitude    float64   `json:"latitude"`
	Longitude   float64   `json:"longitude"`
	DistanceKM  float64   `json:"distance_km"`
	RequestedAt time.Time `json:"requested_at"`
}

// CreateServiceRequestInput is what the mobile app POSTs.
type CreateServiceRequestInput struct {
	VehicleID   uuid.UUID `json:"vehicle_id" binding:"required"`
	CategoryID  uuid.UUID `json:"category_id" binding:"required"`
	Description string    `json:"description"`
	Latitude    float64   `json:"latitude" binding:"required"`
	Longitude   float64   `json:"longitude" binding:"required"`
	// PhotoURLs are Supabase Storage URLs the Flutter app already
	// uploaded to directly — this API never proxies image bytes.
	PhotoURLs []string `json:"photo_urls"`
}

type Offer struct {
	ID               uuid.UUID  `json:"id"`
	ServiceRequestID uuid.UUID  `json:"service_request_id"`
	GarageID         uuid.UUID  `json:"garage_id"`
	MechanicID       *uuid.UUID `json:"mechanic_id,omitempty"`
	Price            string     `json:"price"`
	Currency         string     `json:"currency"`
	EtaMinutes       *int32     `json:"eta_minutes,omitempty"`
	Notes            *string    `json:"notes,omitempty"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
}

type CreateOfferInput struct {
	ServiceRequestID uuid.UUID  `json:"-"` // taken from the URL, not the body
	GarageID         uuid.UUID  `json:"garage_id" `
	MechanicID       *uuid.UUID `json:"mechanic_id,omitempty"`
	Price            string     `json:"price"`
	Currency         string     `json:"currency"`
	EtaMinutes       *int32     `json:"eta_minutes,omitempty"`
	Notes            *string    `json:"notes,omitempty"`
}

type AcceptOfferResult struct {
	ServiceRequestID uuid.UUID
	OfferID          uuid.UUID
	BookingID        uuid.UUID
	GarageID         uuid.UUID
	MechanicID       *uuid.UUID
}

type Booking struct {
	ID               uuid.UUID  `json:"id"`
	ServiceRequestID uuid.UUID  `json:"service_request_id"`
	OfferID          uuid.UUID  `json:"offer_id"`
	GarageID         uuid.UUID  `json:"garage_id"`
	MechanicID       *uuid.UUID `json:"mechanic_id,omitempty"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
}

// --- Reviews ------------------------------------------------------------

type Review struct {
	ID         uuid.UUID `json:"id"`
	BookingID  uuid.UUID `json:"booking_id"`
	ReviewerID uuid.UUID `json:"reviewer_id"`
	Rating     int32     `json:"rating"`
	Comment    *string   `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// CreateReviewInput is what the car owner's app POSTs after a job
// completes. Target is "garage" or "mechanic" — reviews.go's CHECK
// constraint requires exactly one of garage_id/mechanic_id to be set,
// so the service maps Target to the right column rather than trusting
// raw ids from the client.
type CreateReviewInput struct {
	BookingID string `json:"booking_id"`
	Target    string `json:"target"` // "garage" | "mechanic"
	Rating    int32  `json:"rating"`
	Comment   string `json:"comment"`
}
