-- These .sql files are the *source of truth* sqlc reads to generate
-- typed Go code into internal/db/sqlcgen. Run `sqlc generate` (see
-- sqlc.yaml at the repo root) after editing anything here.
--
-- IMPORTANT PostGIS pattern: never SELECT a `geography` column directly —
-- sqlc doesn't know its Go type. Instead always project it into plain
-- scalars with ST_X/ST_Y (or compute ST_Distance/ST_DWithin), which
-- sqlc maps to ordinary float64 columns with zero special-casing.

-- name: ListNearbyGarages :many
select
  id,
  owner_id,
  name,
  address,
  is_verified,
  ST_Y(location::geometry) as latitude,
  ST_X(location::geometry) as longitude,
  ST_Distance(
    location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
  ) as distance_meters
from garages
where is_active = true
  and verification_status = 'approved'
  and ST_DWithin(
    location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
    sqlc.arg(radius_meters)::float8
  )
order by distance_meters asc
limit sqlc.arg(max_results)::int;

-- name: ListNearbyGaragesOfferingCategory :many
-- Same as above but only garages that have opted into the given service
-- category — used both for the car owner's filtered search and for
-- deciding who gets a new_request_match websocket event.
select
  g.id,
  g.owner_id,
  g.name,
  g.address,
  g.is_verified,
  ST_Y(g.location::geometry) as latitude,
  ST_X(g.location::geometry) as longitude,
  ST_Distance(
    g.location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
  ) as distance_meters
from garages g
join garage_service_categories gsc on gsc.garage_id = g.id
where g.is_active = true
  and g.verification_status = 'approved'
  and gsc.category_id = sqlc.arg(category_id)
  and ST_DWithin(
    g.location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
    sqlc.arg(radius_meters)::float8
  )
order by distance_meters asc
limit sqlc.arg(max_results)::int;

-- name: ListPendingGarages :many
select id, owner_id, name, license_number, address, submitted_at
from garages
where verification_status = 'pending'
order by submitted_at asc;

-- name: SetGarageVerificationStatus :exec
update garages
set verification_status = sqlc.arg(status),
    reviewed_at = now(),
    reviewed_by = sqlc.arg(reviewed_by)
where id = sqlc.arg(id);

-- name: AddGarageServiceCategory :exec
insert into garage_service_categories (garage_id, category_id)
values ($1, $2)
on conflict do nothing;

-- name: GetGarage :one
select
  id, owner_id, name, description, address, phone, is_verified, is_active,
  verification_status, license_number,
  ST_Y(location::geometry) as latitude,
  ST_X(location::geometry) as longitude,
  created_at
from garages
where id = $1;

-- name: CreateGarage :one
insert into garages (owner_id, name, description, address, phone, license_number, location)
values (
  $1, $2, $3, $4, $5, $6,
  ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
)
returning id, created_at;
