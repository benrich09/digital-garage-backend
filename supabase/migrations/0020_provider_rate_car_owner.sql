-- Allow providers (garage/mechanic) to rate the car owner after a completed job.
-- Extends reviews so either (garage_id XOR mechanic_id) OR car_owner_id is set.

alter table public.reviews
  add column if not exists car_owner_id uuid references public.profiles(id) on delete cascade;

-- Drop old check and uniqueness that assumed only garage/mechanic targets
alter table public.reviews drop constraint if exists reviews_check;
alter table public.reviews drop constraint if exists reviews_booking_id_reviewer_id_garage_id_mechanic_id_key;

alter table public.reviews
  add constraint reviews_target_check check (
    (
      (garage_id is not null)::int +
      (mechanic_id is not null)::int +
      (car_owner_id is not null)::int
    ) = 1
  );

-- Unique per reviewer + target combination
create unique index if not exists reviews_unique_garage
  on public.reviews (booking_id, reviewer_id, garage_id) where garage_id is not null;
create unique index if not exists reviews_unique_mechanic
  on public.reviews (booking_id, reviewer_id, mechanic_id) where mechanic_id is not null;
create unique index if not exists reviews_unique_car_owner
  on public.reviews (booking_id, reviewer_id, car_owner_id) where car_owner_id is not null;

create index if not exists reviews_car_owner_idx on public.reviews (car_owner_id);

comment on column public.reviews.car_owner_id is
  'Set when a garage/mechanic rates the car owner after job completion.';
