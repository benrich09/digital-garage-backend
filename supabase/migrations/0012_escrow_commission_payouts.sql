-- ---------------------------------------------------------------------
-- 0012: Escrow, commission split, provider payouts, and service pricing.
--
-- This migration turns the money model from "pay the provider after the
-- job" into a real escrow marketplace:
--
--   1. Car owner pays UP FRONT -> funds sit in escrow (held).
--   2. Job completed and CONFIRMED by the car owner -> funds release.
--   3. On release the amount splits: platform commission vs provider net.
--   4. Provider net accrues into a weekly payout batch.
--
-- Commission is stored PER PAYMENT (not read from config at payout time)
-- so that changing the rate later never rewrites history — every past
-- transaction keeps the rate it was actually charged at.
-- ---------------------------------------------------------------------

-- --- Contact phone -----------------------------------------------------
-- Car owner <-> provider need to call each other once a job is matched.
-- Stored on profiles (not garages) because mechanics have one too.
alter table public.profiles
  add column if not exists phone text;

comment on column public.profiles.phone is
  'E.164 mobile number, e.g. +255712345678. Used for direct car-owner <-> provider contact once a booking exists.';

alter table public.profiles
  drop constraint if exists profiles_phone_check;
alter table public.profiles
  add constraint profiles_phone_check
  check (phone is null or phone ~ '^\+[1-9][0-9]{7,14}$');

-- Seed phone at signup alongside full_name/role/gender. Re-declares the
-- trigger function (Postgres has no "add a column to an existing
-- trigger"), keeping every field the earlier migrations added.
create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  insert into public.profiles (id, full_name, role, gender, date_of_birth, phone, email)
  values (
    new.id,
    new.raw_user_meta_data->>'full_name',
    coalesce((new.raw_user_meta_data->>'role')::user_role, 'car_owner'),
    new.raw_user_meta_data->>'gender',
    (new.raw_user_meta_data->>'date_of_birth')::date,
    new.raw_user_meta_data->>'phone',
    new.email
  );
  return new;
end;
$$;

-- --- Escrow state on payments -----------------------------------------
do $$ begin
  create type escrow_status as enum ('none', 'held', 'released', 'refunded');
exception when duplicate_object then null;
end $$;

comment on type escrow_status is
  'none = legacy/direct payment. held = paid by car owner, not yet earned. released = split and credited to provider. refunded = returned to car owner.';

alter table public.payments
  add column if not exists escrow_status     escrow_status not null default 'none',
  -- Rate is a fraction: 0.1500 = 15%. Urgent roadside jobs carry a
  -- higher rate, which is why this is per-payment rather than global.
  add column if not exists commission_rate   numeric(5,4),
  add column if not exists commission_amount numeric(10,2),
  add column if not exists provider_amount   numeric(10,2),
  add column if not exists provider_id       uuid references public.profiles(id) on delete restrict,
  add column if not exists held_at           timestamptz,
  add column if not exists released_at       timestamptz,
  add column if not exists refunded_at       timestamptz,
  add column if not exists payout_id         uuid;

comment on column public.payments.commission_rate is
  'Platform commission as a fraction, frozen at release time. 0.1500 standard, 0.2000 urgent roadside.';
comment on column public.payments.provider_id is
  'The garage owner or mechanic profile that earns provider_amount. Denormalised from the booking so payout queries need no joins.';

-- The split must always add up. Enforced in the DB so no service-layer
-- rounding bug can silently create or destroy money.
alter table public.payments
  drop constraint if exists payments_split_adds_up;
alter table public.payments
  add constraint payments_split_adds_up
  check (
    commission_amount is null
    or provider_amount is null
    or round(commission_amount + provider_amount, 2) = round(amount, 2)
  );

-- Released money must have a provider to credit and a rate on record.
alter table public.payments
  drop constraint if exists payments_released_is_complete;
alter table public.payments
  add constraint payments_released_is_complete
  check (
    escrow_status <> 'released'
    or (provider_id is not null and commission_rate is not null
        and commission_amount is not null and provider_amount is not null)
  );

create index if not exists payments_escrow_status_idx on public.payments (escrow_status);
create index if not exists payments_provider_unpaid_idx
  on public.payments (provider_id, escrow_status)
  where escrow_status = 'released' and payout_id is null;

-- --- Weekly payout batches --------------------------------------------
do $$ begin
  create type payout_status as enum ('pending', 'processing', 'paid', 'failed');
exception when duplicate_object then null;
end $$;

create table if not exists public.payouts (
  id             uuid primary key default gen_random_uuid(),
  provider_id    uuid not null references public.profiles(id) on delete restrict,
  -- Inclusive start, exclusive end. A payment belongs to exactly one
  -- week, so batches can never double-pay.
  period_start   date not null,
  period_end     date not null,
  gross_amount   numeric(12,2) not null check (gross_amount >= 0),
  commission     numeric(12,2) not null check (commission >= 0),
  net_amount     numeric(12,2) not null check (net_amount >= 0),
  currency       text not null default 'TZS',
  status         payout_status not null default 'pending',
  method         payment_method,
  transaction_ref text,
  failure_reason text,
  paid_at        timestamptz,
  created_at     timestamptz not null default now(),
  constraint payouts_period_valid check (period_end > period_start),
  constraint payouts_net_adds_up check (round(commission + net_amount, 2) = round(gross_amount, 2)),
  constraint payouts_one_per_provider_per_period unique (provider_id, period_start)
);

comment on table public.payouts is
  'One weekly batch per provider. Created by the payout job, marked paid once the disbursement clears.';

create index if not exists payouts_provider_idx on public.payouts (provider_id, period_start desc);
create index if not exists payouts_status_idx on public.payouts (status);

alter table public.payments
  drop constraint if exists payments_payout_fk;
alter table public.payments
  add constraint payments_payout_fk
  foreign key (payout_id) references public.payouts(id) on delete set null;

-- --- Provider service price list --------------------------------------
-- "Providers set their own service prices." Each provider owns rows here;
-- car owners read them when choosing a service.
create table if not exists public.provider_services (
  id           uuid primary key default gen_random_uuid(),
  provider_id  uuid not null references public.profiles(id) on delete cascade,
  garage_id    uuid references public.garages(id) on delete cascade,
  name         text not null check (length(trim(name)) between 2 and 120),
  description  text,
  price        numeric(10,2) not null check (price >= 0),
  currency     text not null default 'TZS',
  -- Roadside work is priced and commissioned differently from workshop
  -- work, so the distinction lives on the service itself.
  is_roadside  boolean not null default false,
  duration_minutes integer check (duration_minutes is null or duration_minutes > 0),
  is_active    boolean not null default true,
  created_at   timestamptz not null default now(),
  updated_at   timestamptz not null default now(),
  constraint provider_services_unique_name unique (provider_id, name)
);

comment on table public.provider_services is
  'Self-managed price list. Car owners pick from these when booking; the chosen row fixes the offer amount.';

create index if not exists provider_services_provider_idx
  on public.provider_services (provider_id) where is_active;
create index if not exists provider_services_garage_idx
  on public.provider_services (garage_id) where is_active;

-- --- Car owner activity history ---------------------------------------
-- Backs the History tab. Kept separate from service_requests so the user
-- can clear their history without destroying operational records.
create table if not exists public.activity_log (
  id           uuid primary key default gen_random_uuid(),
  user_id      uuid not null references public.profiles(id) on delete cascade,
  kind         text not null check (kind in (
                 'request_created', 'offer_received', 'offer_accepted',
                 'booking_started', 'booking_completed', 'payment_made',
                 'payout_received', 'review_left')),
  title        text not null,
  subtitle     text,
  request_id   uuid references public.service_requests(id) on delete set null,
  booking_id   uuid references public.bookings(id) on delete set null,
  created_at   timestamptz not null default now()
);

create index if not exists activity_log_user_idx on public.activity_log (user_id, created_at desc);

-- --- RLS ---------------------------------------------------------------
alter table public.payouts           enable row level security;
alter table public.provider_services enable row level security;
alter table public.activity_log      enable row level security;

-- Providers read their own payouts; nobody writes them from the client
-- (the payout job uses the service role, which bypasses RLS).
drop policy if exists payouts_select_own on public.payouts;
create policy payouts_select_own on public.payouts
  for select using (provider_id = auth.uid());

-- Price lists are world-readable when active (car owners must browse
-- them) but only the owning provider may modify them.
drop policy if exists provider_services_select_active on public.provider_services;
create policy provider_services_select_active on public.provider_services
  for select using (is_active or provider_id = auth.uid());

drop policy if exists provider_services_write_own on public.provider_services;
create policy provider_services_write_own on public.provider_services
  for all using (provider_id = auth.uid()) with check (provider_id = auth.uid());

-- History is strictly private, and the user may clear it.
drop policy if exists activity_log_own on public.activity_log;
create policy activity_log_own on public.activity_log
  for all using (user_id = auth.uid()) with check (user_id = auth.uid());

-- --- Provider earnings view -------------------------------------------
-- Backs the provider Statistics tab and the admin dashboard in one query
-- instead of three round trips.
create or replace view public.provider_earnings as
select
  p.provider_id,
  count(*)                                                     as jobs_paid,
  coalesce(sum(p.amount), 0)                                   as gross_earned,
  coalesce(sum(p.commission_amount), 0)                        as commission_paid,
  coalesce(sum(p.provider_amount), 0)                          as net_earned,
  coalesce(sum(p.provider_amount) filter (where p.payout_id is null), 0) as pending_payout,
  coalesce(sum(p.provider_amount) filter (where p.payout_id is not null), 0) as paid_out
from public.payments p
where p.escrow_status = 'released' and p.provider_id is not null
group by p.provider_id;

comment on view public.provider_earnings is
  'Per-provider earnings rollup. pending_payout is what the next weekly batch will contain.';
