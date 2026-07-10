-- name: CreateOffer :one
insert into offers (service_request_id, garage_id, mechanic_id, price, currency, eta_minutes, notes)
values ($1, $2, $3, $4, $5, $6, $7)
returning id, status, created_at;

-- name: GetOffer :one
select id, service_request_id, garage_id, mechanic_id, price, currency, eta_minutes, notes, status, created_at
from offers
where id = $1;

-- name: ListOffersForRequest :many
select id, service_request_id, garage_id, mechanic_id, price, currency, eta_minutes, notes, status, created_at
from offers
where service_request_id = $1
order by created_at asc;

-- name: SetOfferStatus :exec
update offers
set status = $2, responded_at = now()
where id = $1;

-- name: RejectOtherOffers :exec
-- Called inside the same transaction as accepting the winning offer.
update offers
set status = 'rejected', responded_at = now()
where service_request_id = $1
  and id <> $2
  and status = 'pending';
