-- name: UpdateMechanicLocation :exec
update mechanics
set current_location = ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography,
    location_updated_at = now()
where id = sqlc.arg(id);

-- name: InsertMechanicLocationHistory :exec
insert into mechanic_location_history (mechanic_id, booking_id, location)
values (
  sqlc.arg(mechanic_id),
  sqlc.arg(booking_id),
  ST_SetSRID(ST_MakePoint(sqlc.arg(lng)::float8, sqlc.arg(lat)::float8), 4326)::geography
);

-- name: GetMechanicByProfileID :one
select id, profile_id, garage_id, specialty, is_available
from mechanics
where profile_id = $1;
