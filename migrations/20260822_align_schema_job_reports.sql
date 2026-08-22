-- =============================================================================
-- Smart Garage — align Supabase with job lifecycle + incident reports
-- Safe to re-run. Run in Supabase SQL Editor on project thenoifpvygqjdxgtehz
-- =============================================================================

-- 1) service_requests.request_kind (routing mechanic vs garage)
alter table public.service_requests
  add column if not exists request_kind text;

comment on column public.service_requests.request_kind is
  'mechanic_request | garage_booking';

-- 2) bookings lifecycle columns
alter table public.bookings add column if not exists started_at timestamptz;
alter table public.bookings add column if not exists completed_at timestamptz;
alter table public.bookings add column if not exists bill_amount numeric(14,2);
alter table public.bookings add column if not exists currency text default 'TZS';
alter table public.bookings add column if not exists payment_confirmed boolean default false;
alter table public.bookings add column if not exists customer_satisfied boolean default false;

-- 3) Extend booking_status enum when it is an enum type
do $$
declare
  t text;
  vals text[] := array[
    'scheduled','en_route','awaiting_customer','arrived','in_progress',
    'completed','awaiting_satisfaction','billed','awaiting_payment','paid','closed','cancelled'
  ];
  v text;
begin
  select data_type into t
  from information_schema.columns
  where table_schema = 'public' and table_name = 'bookings' and column_name = 'status';

  if t = 'USER-DEFINED' then
    foreach v in array vals loop
      begin
        execute format('alter type booking_status add value if not exists %L', v);
      exception when others then
        -- older PG without IF NOT EXISTS on enum: try plain add
        begin
          execute format('alter type booking_status add value %L', v);
        exception when duplicate_object then
          null;
        when others then
          null;
        end;
      end;
    end loop;
  end if;
end $$;

-- 4) Incident reports (customer ↔ provider ↔ admin)
create table if not exists public.incident_reports (
  id uuid primary key default gen_random_uuid(),
  reporter_id uuid not null references public.profiles(id) on delete cascade,
  against_user_id uuid references public.profiles(id) on delete set null,
  booking_id uuid references public.bookings(id) on delete set null,
  service_request_id uuid references public.service_requests(id) on delete set null,
  category text not null default 'other',
  description text not null,
  status text not null default 'open'
    check (status in ('open','reviewing','resolved','dismissed')),
  admin_notes text,
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create index if not exists incident_reports_reporter_idx on public.incident_reports(reporter_id);
create index if not exists incident_reports_status_idx on public.incident_reports(status);
create index if not exists incident_reports_created_idx on public.incident_reports(created_at desc);

-- RLS: reporters see own rows; service role / backend bypasses RLS
alter table public.incident_reports enable row level security;

drop policy if exists incident_reports_select_own on public.incident_reports;
create policy incident_reports_select_own on public.incident_reports
  for select using (auth.uid() = reporter_id);

drop policy if exists incident_reports_insert_own on public.incident_reports;
create policy incident_reports_insert_own on public.incident_reports
  for insert with check (auth.uid() = reporter_id);

-- 5) Helpful index for open-request inbox
create index if not exists service_requests_status_requested_idx
  on public.service_requests (status, requested_at desc);

create index if not exists service_requests_request_kind_idx
  on public.service_requests (request_kind)
  where request_kind is not null;

-- 6) Realtime (optional — ignore errors if already added)
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
