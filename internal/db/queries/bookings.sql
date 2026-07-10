-- name: CreateBooking :one
insert into bookings (service_request_id, offer_id, garage_id, mechanic_id, scheduled_time)
values ($1, $2, $3, $4, $5)
returning id, status, created_at;

-- name: GetBookingByRequest :one
select id, service_request_id, offer_id, garage_id, mechanic_id, status,
       scheduled_time, started_at, completed_at, created_at
from bookings
where service_request_id = $1;

-- name: GetBooking :one
select id, service_request_id, offer_id, garage_id, mechanic_id, status,
       scheduled_time, started_at, completed_at, created_at
from bookings
where id = $1;

-- name: SetBookingStatus :exec
update bookings
set status = $2,
    started_at = case when $2 = 'in_progress' then now() else started_at end,
    completed_at = case when $2 = 'completed' then now() else completed_at end,
    updated_at = now()
where id = $1;
