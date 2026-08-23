-- =============================================================================
-- 0020 — Job lifecycle, mutual reviews, incident reports, request routing
-- Safe to re-run (IF NOT EXISTS / exception guards).
-- Covers: booking sequence, mechanic vs garage routing, ratings, complaints.
-- =============================================================================

-- ---------------------------------------------------------------------------
-- 1) service_requests: kind, preferred garage, schedule, soft-hide
-- ---------------------------------------------------------------------------
alter table public.service_requests
  add column if not exists request_kind text;

alter table public.service_requests
  add column if not exists preferred_garage_id uuid references public.garages(id) on delete set null;

alter table public.service_requests
  add column if not exists scheduled_at timestamptz;

alter table public.service_requests
  add column if not exists hidden_by_owner boolean not null default false;

alter table public.service_requests
  add column if not exists hidden_by_provider boolean not null default false;

-- Default kind for legacy rows
update public.service_requests
set request_kind = coalesce(nullif(trim(request_kind), ''), 'mechanic_request')
where request_kind is null or trim(request_kind) = '';

comment on column public.service_requests.request_kind is
  'mechanic_request | garage_booking — routes inbox to mechanic vs garage accounts';
comment on column public.service_requests.preferred_garage_id is
  'When set, garage_booking is aimed at this garage owner';

create index if not exists service_requests_status_kind_idx
  on public.service_requests (status, request_kind);

create index if not exists service_requests_preferred_garage_idx
  on public.service_requests (preferred_garage_id)
  where preferred_garage_id is not null;

create index if not exists service_requests_status_requested_idx
  on public.service_requests (status, requested_at desc);

-- ---------------------------------------------------------------------------
-- 2) bookings: full job lifecycle columns
-- ---------------------------------------------------------------------------
alter table public.bookings add column if not exists started_at timestamptz;
alter table public.bookings add column if not exists completed_at timestamptz;
alter table public.bookings add column if not exists bill_amount numeric(14, 2);
alter table public.bookings add column if not exists currency text default 'TZS';
alter table public.bookings add column if not exists payment_confirmed boolean default false;
alter table public.bookings add column if not exists customer_satisfied boolean default false;

comment on column public.bookings.bill_amount is 'Amount set by provider after customer satisfaction';
comment on column public.bookings.customer_satisfied is 'Customer confirmed satisfaction before bill';
comment on column public.bookings.payment_confirmed is 'Provider confirmed customer payment received';

create index if not exists bookings_status_idx on public.bookings (status);
create index if not exists bookings_service_request_idx on public.bookings (service_request_id);

-- RLS: car owner may update satisfaction / claim paid on own bookings
alter table public.bookings enable row level security;

drop policy if exists bookings_owner_update_lifecycle on public.bookings;
create policy bookings_owner_update_lifecycle on public.bookings
  for update to authenticated
  using (
    exists (
      select 1 from public.service_requests sr
      where sr.id = bookings.service_request_id
        and sr.car_owner_id = auth.uid()
    )
  )
  with check (
    exists (
      select 1 from public.service_requests sr
      where sr.id = bookings.service_request_id
        and sr.car_owner_id = auth.uid()
    )
  );

-- ---------------------------------------------------------------------------
-- 3) reviews: mutual rating (provider ↔ car owner)
-- ---------------------------------------------------------------------------
alter table public.reviews
  add column if not exists car_owner_id uuid references public.profiles(id) on delete cascade;

alter table public.reviews drop constraint if exists reviews_check;
alter table public.reviews drop constraint if exists reviews_target_check;
alter table public.reviews drop constraint if exists reviews_booking_id_reviewer_id_garage_id_mechanic_id_key;

-- Soft target check: at most one of garage / mechanic / car_owner (0 allowed for simple rating rows)
do $$
begin
  alter table public.reviews
    add constraint reviews_target_check check (
      (
        (garage_id is not null)::int +
        (mechanic_id is not null)::int +
        (car_owner_id is not null)::int
      ) <= 1
    );
exception when duplicate_object then null;
end $$;

create unique index if not exists reviews_unique_garage
  on public.reviews (booking_id, reviewer_id, garage_id)
  where garage_id is not null;

create unique index if not exists reviews_unique_mechanic
  on public.reviews (booking_id, reviewer_id, mechanic_id)
  where mechanic_id is not null;

create unique index if not exists reviews_unique_car_owner
  on public.reviews (booking_id, reviewer_id, car_owner_id)
  where car_owner_id is not null;

-- One simple rating per reviewer per booking when no target FK set
create unique index if not exists reviews_unique_reviewer_booking
  on public.reviews (booking_id, reviewer_id)
  where garage_id is null and mechanic_id is null and car_owner_id is null;

create index if not exists reviews_car_owner_idx on public.reviews (car_owner_id);

comment on column public.reviews.car_owner_id is
  'Set when a garage/mechanic rates the car owner after job completion';

-- ---------------------------------------------------------------------------
-- 4) incident_reports (complaints)
-- ---------------------------------------------------------------------------
create table if not exists public.incident_reports (
  id uuid primary key default gen_random_uuid(),
  reporter_id uuid not null references public.profiles(id) on delete cascade,
  against_user_id uuid references public.profiles(id) on delete set null,
  booking_id uuid references public.bookings(id) on delete set null,
  service_request_id uuid references public.service_requests(id) on delete set null,
  category text not null default 'other',
  description text not null,
  status text not null default 'open'
    check (status in ('open', 'reviewing', 'resolved', 'dismissed')),
  admin_notes text,
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create index if not exists incident_reports_reporter_idx on public.incident_reports (reporter_id);
create index if not exists incident_reports_status_idx on public.incident_reports (status);
create index if not exists incident_reports_created_idx on public.incident_reports (created_at desc);

alter table public.incident_reports enable row level security;

drop policy if exists incident_reports_select_own on public.incident_reports;
create policy incident_reports_select_own on public.incident_reports
  for select to authenticated
  using (
    auth.uid() = reporter_id
    or exists (
      select 1 from public.profiles p
      where p.id = auth.uid() and p.role in ('admin', 'superadmin')
    )
  );

drop policy if exists incident_reports_insert_own on public.incident_reports;
create policy incident_reports_insert_own on public.incident_reports
  for insert to authenticated
  with check (auth.uid() = reporter_id);

-- ---------------------------------------------------------------------------
-- 5) Realtime (ignore if already published)
-- ---------------------------------------------------------------------------
do $$
begin
  alter publication supabase_realtime add table public.service_requests;
exception when others then null;
end $$;
do $$
begin
  alter publication supabase_realtime add table public.bookings;
exception when others then null;
end $$;
do $$
begin
  alter publication supabase_realtime add table public.incident_reports;
exception when others then null;
end $$;
