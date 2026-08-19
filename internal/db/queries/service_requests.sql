-- name: CreateServiceRequest :one
insert into service_requests (car_owner_id, vehicle_id, category_id, description, pickup_location, photo_urls)
values (
  $1, $2, $3, $4,
  ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
  sqlc.arg(photo_urls)::jsonb
)
returning id, status, created_at;

-- name: GetServiceRequest :one
select
  id, car_owner_id, vehicle_id, category_id, description, status, photo_urls,
  ST_Y(pickup_location::geometry) as latitude,
  ST_X(pickup_location::geometry) as longitude,
  requested_at, scheduled_at, created_at
from service_requests
where id = $1;

-- name: ListServiceRequestsByOwner :many
select
  id, vehicle_id, category_id, description, status, requested_at, scheduled_at
from service_requests
where car_owner_id = $1
order by requested_at desc
limit sqlc.arg(max_results)::int;

-- name: ListOpenServiceRequestsNear :many
-- Used by the garage/mechanic app to browse open (status = 'pending')
-- requests within a radius, closest first. Includes full car-owner
-- profile + vehicle so the provider can decide whether to accept.
select
  sr.id, sr.description, sr.status, sr.requested_at, sr.category_id,
  ST_Y(sr.pickup_location::geometry) as latitude,
  ST_X(sr.pickup_location::geometry) as longitude,
  ST_Distance(
    sr.pickup_location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
  ) as distance_meters,
  p.id as owner_id,
  p.full_name as owner_name,
  p.phone as owner_phone,
  p.avatar_url as owner_avatar_url,
  v.id as vehicle_id,
  v.make as vehicle_make,
  v.model as vehicle_model,
  v.year as vehicle_year,
  v.plate_number as vehicle_plate,
  sc.name as category_name
from service_requests sr
join profiles p on p.id = sr.car_owner_id
join vehicles v on v.id = sr.vehicle_id
left join service_categories sc on sc.id = sr.category_id
where sr.status = 'pending'
  and ST_DWithin(
    sr.pickup_location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
    sqlc.arg(radius_meters)::float8
  )
order by distance_meters asc
limit sqlc.arg(max_results)::int;

-- name: UpdateServiceRequestStatus :exec
update service_requests
set status = $2, updated_at = now()
where id = $1;

-- name: CancelServiceRequest :exec
-- Owner-only cancel while still pending/quoted (enforced in service layer).
update service_requests
set status = 'cancelled', updated_at = now()
where id = $1
  and car_owner_id = $2
  and status in ('pending', 'quoted');

-- name: ListNearbyAvailableMechanics :many
-- Independent/field mechanics near a point who are marked available.
-- Notifies them of new requests the same way garage owners are notified.
select
  m.id as mechanic_id,
  m.profile_id,
  m.garage_id,
  ST_Y(m.current_location::geometry) as latitude,
  ST_X(m.current_location::geometry) as longitude,
  ST_Distance(
    m.current_location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
  ) as distance_meters
from mechanics m
-- is_available no longer required for matching
  and m.current_location is not null
  and ST_DWithin(
    m.current_location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
    sqlc.arg(radius_meters)::float8
  )
order by distance_meters asc
limit sqlc.arg(max_results)::int;
