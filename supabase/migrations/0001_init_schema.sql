-- =====================================================================
-- MIGRATION 0001: CORE SCHEMA
-- Digital Garage Marketplace — extensions, enums, tables, indexes.
-- =====================================================================

create extension if not exists postgis;
create extension if not exists "uuid-ossp";
create extension if not exists pgcrypto;

create type user_role as enum ('car_owner', 'garage_owner', 'mechanic', 'admin');

create type request_status as enum (
  'pending', 'quoted', 'accepted', 'in_progress', 'completed',
  'paid', 'closed', 'cancelled', 'expired', 'disputed'
);

create type offer_status as enum ('pending', 'accepted', 'rejected', 'withdrawn', 'expired');
create type booking_status as enum ('scheduled', 'in_progress', 'completed', 'cancelled');
create type payment_status as enum ('pending', 'paid', 'failed', 'refunded', 'partially_refunded');
create type payment_method as enum ('card', 'mobile_money', 'cash', 'wallet');

-- ---------------------------------------------------------------------
create table public.profiles (
  id           uuid primary key references auth.users(id) on delete cascade,
  role         user_role not null default 'car_owner',
  full_name    text not null,
  phone        text unique,
  avatar_url   text,
  is_active    boolean not null default true,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now()
);
comment on table public.profiles is
  'App-level user data extending auth.users. One row per authenticated user.';

create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  insert into public.profiles (id, full_name, role)
  values (
    new.id,
    coalesce(new.raw_user_meta_data->>'full_name', 'New User'),
    coalesce((new.raw_user_meta_data->>'role')::user_role, 'car_owner')
  );
  return new;
end;
$$;

create trigger on_auth_user_created
  after insert on auth.users
  for each row execute procedure public.handle_new_user();

-- ---------------------------------------------------------------------
create table public.garages (
  id            uuid primary key default gen_random_uuid(),
  owner_id      uuid not null references public.profiles(id) on delete restrict,
  name          text not null,
  description   text,
  address       text,
  location      geography(Point, 4326) not null,
  phone         text,
  is_verified   boolean not null default false,
  is_active     boolean not null default true,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now()
);
comment on table public.garages is 'Physical repair shops, owned by a garage_owner profile.';

create index garages_location_gix on public.garages using gist (location);
create index garages_owner_idx on public.garages (owner_id);

-- ---------------------------------------------------------------------
create table public.mechanics (
  id                  uuid primary key default gen_random_uuid(),
  profile_id          uuid not null unique references public.profiles(id) on delete cascade,
  garage_id           uuid not null references public.garages(id) on delete cascade,
  specialty           text,
  is_available        boolean not null default true,
  current_location    geography(Point, 4326),
  location_updated_at timestamptz,
  created_at          timestamptz not null default now()
);
comment on table public.mechanics is
  'Mechanics belonging to a garage. current_location is live GPS during active bookings only.';

create index mechanics_location_gix on public.mechanics using gist (current_location);
create index mechanics_garage_idx on public.mechanics (garage_id);

create table public.mechanic_location_history (
  id           bigint generated always as identity primary key,
  mechanic_id  uuid not null references public.mechanics(id) on delete cascade,
  booking_id   uuid,
  location     geography(Point, 4326) not null,
  recorded_at  timestamptz not null default now()
);
comment on table public.mechanic_location_history is
  'Breadcrumb trail of mechanic locations during active bookings.';

create index mechanic_location_history_gix on public.mechanic_location_history using gist (location);
create index mechanic_location_history_mechanic_idx on public.mechanic_location_history (mechanic_id, recorded_at);

-- ---------------------------------------------------------------------
create table public.vehicles (
  id            uuid primary key default gen_random_uuid(),
  owner_id      uuid not null references public.profiles(id) on delete cascade,
  make          text not null,
  model         text not null,
  year          smallint,
  plate_number  text,
  vin           text,
  color         text,
  created_at    timestamptz not null default now(),
  unique (owner_id, plate_number)
);
comment on table public.vehicles is 'Vehicles registered by car owners.';

create index vehicles_owner_idx on public.vehicles (owner_id);

-- ---------------------------------------------------------------------
create table public.service_categories (
  id          uuid primary key default gen_random_uuid(),
  name        text not null unique,
  description text,
  icon_url    text,
  is_active   boolean not null default true
);
comment on table public.service_categories is 'Lookup table of service types offered on the platform.';

-- ---------------------------------------------------------------------
create table public.service_requests (
  id               uuid primary key default gen_random_uuid(),
  car_owner_id     uuid not null references public.profiles(id) on delete cascade,
  vehicle_id       uuid not null references public.vehicles(id) on delete restrict,
  category_id      uuid not null references public.service_categories(id) on delete restrict,
  description      text,
  pickup_location  geography(Point, 4326) not null,
  status           request_status not null default 'pending',
  requested_at     timestamptz not null default now(),
  scheduled_at     timestamptz,
  cancelled_reason text,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);
comment on table public.service_requests is
  'A car owner''s request for service; status drives the lifecycle state machine.';

create index service_requests_location_gix on public.service_requests using gist (pickup_location);
create index service_requests_owner_idx on public.service_requests (car_owner_id);
create index service_requests_status_idx on public.service_requests (status);
create index service_requests_category_idx on public.service_requests (category_id);

-- ---------------------------------------------------------------------
create table public.offers (
  id                 uuid primary key default gen_random_uuid(),
  service_request_id uuid not null references public.service_requests(id) on delete cascade,
  garage_id          uuid not null references public.garages(id) on delete cascade,
  mechanic_id        uuid references public.mechanics(id) on delete set null,
  price              numeric(10,2) not null check (price >= 0),
  currency           text not null default 'TZS',
  eta_minutes        integer check (eta_minutes >= 0),
  notes              text,
  status             offer_status not null default 'pending',
  created_at         timestamptz not null default now(),
  responded_at       timestamptz,
  unique (service_request_id, garage_id, mechanic_id)
);
comment on table public.offers is 'Quotes submitted by garages against a service request.';

create index offers_request_idx on public.offers (service_request_id);
create index offers_garage_idx on public.offers (garage_id);
create index offers_status_idx on public.offers (status);

-- ---------------------------------------------------------------------
create table public.bookings (
  id                 uuid primary key default gen_random_uuid(),
  service_request_id uuid not null unique references public.service_requests(id) on delete cascade,
  offer_id           uuid not null unique references public.offers(id) on delete restrict,
  garage_id          uuid not null references public.garages(id) on delete restrict,
  mechanic_id        uuid references public.mechanics(id) on delete set null,
  status             booking_status not null default 'scheduled',
  scheduled_time     timestamptz,
  started_at         timestamptz,
  completed_at       timestamptz,
  cancelled_reason   text,
  created_at         timestamptz not null default now(),
  updated_at         timestamptz not null default now()
);
comment on table public.bookings is 'Confirmed work order created from an accepted offer.';

create index bookings_garage_idx on public.bookings (garage_id);
create index bookings_mechanic_idx on public.bookings (mechanic_id);
create index bookings_status_idx on public.bookings (status);

alter table public.mechanic_location_history
  add constraint mechanic_location_history_booking_fk
  foreign key (booking_id) references public.bookings(id) on delete cascade;

-- ---------------------------------------------------------------------
create table public.payments (
  id              uuid primary key default gen_random_uuid(),
  booking_id      uuid not null references public.bookings(id) on delete restrict,
  amount          numeric(10,2) not null check (amount >= 0),
  currency        text not null default 'TZS',
  method          payment_method not null,
  status          payment_status not null default 'pending',
  transaction_ref text,
  paid_at         timestamptz,
  created_at      timestamptz not null default now()
);
comment on table public.payments is 'Payment record tied to a completed booking.';

create index payments_booking_idx on public.payments (booking_id);
create index payments_status_idx on public.payments (status);

-- ---------------------------------------------------------------------
create table public.reviews (
  id            uuid primary key default gen_random_uuid(),
  booking_id    uuid not null references public.bookings(id) on delete cascade,
  reviewer_id   uuid not null references public.profiles(id) on delete cascade,
  garage_id     uuid references public.garages(id) on delete cascade,
  mechanic_id   uuid references public.mechanics(id) on delete cascade,
  rating        smallint not null check (rating between 1 and 5),
  comment       text,
  created_at    timestamptz not null default now(),
  unique (booking_id, reviewer_id, garage_id, mechanic_id),
  check (
    (garage_id is not null and mechanic_id is null) or
    (garage_id is null and mechanic_id is not null)
  )
);
comment on table public.reviews is 'Post-booking rating/review about a garage or mechanic.';

create index reviews_garage_idx on public.reviews (garage_id);
create index reviews_mechanic_idx on public.reviews (mechanic_id);

-- ---------------------------------------------------------------------
create or replace function public.set_updated_at()
returns trigger language plpgsql as $$
begin
  new.updated_at = now();
  return new;
end;
$$;

create trigger trg_profiles_updated_at before update on public.profiles
  for each row execute procedure public.set_updated_at();
create trigger trg_garages_updated_at before update on public.garages
  for each row execute procedure public.set_updated_at();
create trigger trg_service_requests_updated_at before update on public.service_requests
  for each row execute procedure public.set_updated_at();
create trigger trg_bookings_updated_at before update on public.bookings
  for each row execute procedure public.set_updated_at();

-- ---------------------------------------------------------------------
-- Enable RLS everywhere now; policies are added in migration 0002.
alter table public.profiles enable row level security;
alter table public.garages enable row level security;
alter table public.mechanics enable row level security;
alter table public.mechanic_location_history enable row level security;
alter table public.vehicles enable row level security;
alter table public.service_categories enable row level security;
alter table public.service_requests enable row level security;
alter table public.offers enable row level security;
alter table public.bookings enable row level security;
alter table public.payments enable row level security;
alter table public.reviews enable row level security;

-- ---------------------------------------------------------------------
-- Seed reference data
insert into public.service_categories (name, description) values
  ('Oil Change', 'Engine oil and filter replacement'),
  ('Towing', 'Vehicle towing to garage or roadside recovery'),
  ('Brake Repair', 'Brake pad/disc inspection and replacement'),
  ('Battery Service', 'Battery testing, jump-start, replacement'),
  ('Diagnostics', 'Engine/electrical fault diagnosis'),
  ('Tire Service', 'Puncture repair, tire replacement, rotation');
