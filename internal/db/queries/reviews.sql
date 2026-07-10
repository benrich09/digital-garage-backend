-- name: CreateReview :one
insert into reviews (booking_id, reviewer_id, garage_id, mechanic_id, rating, comment)
values ($1, $2, $3, $4, $5, $6)
returning id, created_at;

-- name: ReviewExists :one
select exists(
  select 1 from reviews
  where booking_id = $1 and reviewer_id = $2
    and coalesce(garage_id, '00000000-0000-0000-0000-000000000000') = coalesce($3, '00000000-0000-0000-0000-000000000000')
    and coalesce(mechanic_id, '00000000-0000-0000-0000-000000000000') = coalesce($4, '00000000-0000-0000-0000-000000000000')
) as exists;

-- name: ListReviewsForGarage :many
select id, booking_id, reviewer_id, rating, comment, created_at
from reviews
where garage_id = $1
order by created_at desc
limit sqlc.arg(max_results)::int;

-- name: ListReviewsForMechanic :many
select id, booking_id, reviewer_id, rating, comment, created_at
from reviews
where mechanic_id = $1
order by created_at desc
limit sqlc.arg(max_results)::int;
