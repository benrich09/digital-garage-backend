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

// OpenServiceRequest is what a provider sees when browsing open
// (pending) work near them. Includes the car-owner profile and vehicle
// so the provider can decide whether to send an offer.
type OpenServiceRequest struct {
	ID             uuid.UUID `json:"id"`
	Description    *string   `json:"description,omitempty"`
	Status         string    `json:"status"`
	RequestKind    string    `json:"request_kind"`
	CategoryID     uuid.UUID `json:"category_id"`
	CategoryName   *string   `json:"category_name,omitempty"`
	Latitude       float64   `json:"latitude"`
	Longitude      float64   `json:"longitude"`
	DistanceKM     float64   `json:"distance_km"`
	RequestedAt    time.Time `json:"requested_at"`
	OwnerID        uuid.UUID `json:"owner_id"`
	OwnerName      *string   `json:"owner_name,omitempty"`
	OwnerPhone     *string   `json:"owner_phone,omitempty"`
	OwnerAvatarURL *string   `json:"owner_avatar_url,omitempty"`
	VehicleID      uuid.UUID `json:"vehicle_id"`
	VehicleMake    *string   `json:"vehicle_make,omitempty"`
	VehicleModel   *string   `json:"vehicle_model,omitempty"`
	VehicleYear    *int32    `json:"vehicle_year,omitempty"`
	VehiclePlate   *string   `json:"vehicle_plate,omitempty"`
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
	// RequestKind: "garage_booking" | "mechanic_request"
	RequestKind string `json:"request_kind"`
	// LocationMode: "on_road" | "at_home" (mechanic requests)
	LocationMode       string     `json:"location_mode"`
	PreferredGarageID  *uuid.UUID `json:"preferred_garage_id"`
	PreferredServiceID *uuid.UUID `json:"preferred_service_id"`
	CarType            string     `json:"car_type"`
	ScheduledAt        *string    `json:"scheduled_at"` // RFC3339
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
	// ScheduledTime is when a garage booking is due to start; StartedAt /
	// CompletedAt are stamped by SetBookingStatus when the provider taps
	// "Start service" / "Finish service". The apps compute the live
	// service duration as (now|CompletedAt) - StartedAt, so surfacing
	// these three is what powers the on-screen service timer.
	ScheduledTime *time.Time `json:"scheduled_time,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
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

// CreateReviewInput is POSTed after a job.
// target: "garage" | "mechanic" (car owner) or "car_owner" (provider rates customer).
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
