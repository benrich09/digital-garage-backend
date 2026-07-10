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
-- requests within a radius, closest first.
select
  sr.id, sr.description, sr.status, sr.requested_at,
  ST_Y(sr.pickup_location::geometry) as latitude,
  ST_X(sr.pickup_location::geometry) as longitude,
  ST_Distance(
    sr.pickup_location,
    ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
  ) as distance_meters
from service_requests sr
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
