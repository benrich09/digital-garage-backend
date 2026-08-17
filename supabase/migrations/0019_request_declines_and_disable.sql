-- ---------------------------------------------------------------------
-- 0019: Silent declines + admin force-disable for unpaid debt
--
-- * request_declines  — tracks which providers soft-declined a request
--   so the matcher does not keep re-notifying the same mechanic, and so
--   a deny stays invisible to the car owner (request stays "pending").
-- * Admin can set profiles.is_active = false (already on the table) via
--   a dedicated endpoint; this migration only adds the index that makes
--   "list inactive debtors" cheap.
-- ---------------------------------------------------------------------

create table if not exists public.request_declines (
  id                 uuid primary key default gen_random_uuid(),
  service_request_id uuid not null references public.service_requests(id) on delete cascade,
  provider_id        uuid not null references public.profiles(id) on delete cascade,
  reason             text,
  created_at         timestamptz not null default now(),
  unique (service_request_id, provider_id)
);

comment on table public.request_declines is
  'Soft declines by garage_owner / mechanic. Request stays open for other providers; car owner is never told about individual denials.';

create index if not exists request_declines_request_idx
  on public.request_declines (service_request_id);

create index if not exists request_declines_provider_idx
  on public.request_declines (provider_id);

-- RLS: providers can insert their own decline; owners/admins can read.
alter table public.request_declines enable row level security;

drop policy if exists request_declines_insert_own on public.request_declines;
create policy request_declines_insert_own on public.request_declines
  for insert to authenticated
  with check (provider_id = auth.uid());

drop policy if exists request_declines_select_parties on public.request_declines;
create policy request_declines_select_parties on public.request_declines
  for select to authenticated
  using (
    provider_id = auth.uid()
    or exists (
      select 1 from public.service_requests sr
      where sr.id = service_request_id and sr.car_owner_id = auth.uid()
    )
    or exists (
      select 1 from public.profiles p
      where p.id = auth.uid() and p.role in ('admin', 'superadmin')
    )
  );

-- Speed up admin "disable for unpaid debt" lookups.
create index if not exists profiles_is_active_role_idx
  on public.profiles (is_active, role)
  where is_active = false;
