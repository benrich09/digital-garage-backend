package models

import "github.com/google/uuid"

// AuthUser is what RequireAuth + LoadProfile leave in the request
// context: the verified JWT subject joined against public.profiles for
// the role that drives RBAC decisions.
type AuthUser struct {
	ID       uuid.UUID
	Role     string
	FullName string
}

const (
	RoleCarOwner    = "car_owner"
	RoleGarageOwner = "garage_owner"
	RoleMechanic    = "mechanic"
	RoleAdmin       = "admin"
)

// GarageVerificationInput is what a garage_owner submits after signing
// up, before an admin approves them.
type GarageVerificationInput struct {
	Name          string   `json:"name" `
	LicenseNumber string   `json:"license_number"`
	Address       string   `json:"address"`
	Latitude      float64  `json:"latitude"`
	Longitude     float64  `json:"longitude"`
	CategoryIDs   []string `json:"category_ids"` // service categories this garage offers
}
