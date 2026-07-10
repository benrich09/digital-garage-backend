-- =====================================================================
-- MIGRATION 0004: GARAGE VERIFICATION + CATEGORY MATCHING + PHOTOS
-- =====================================================================

create type garage_verification_status as enum ('pending', 'approved', 'rejected');

alter table public.garages
  add column license_number       text,
  add column verification_status  garage_verification_status not null default 'pending',
  add column submitted_at         timestamptz not null default now(),
  add column reviewed_at          timestamptz,
  add column reviewed_by          uuid references public.profiles(id);

comment on column public.garages.verification_status is
  'pending until an admin approves the garage_owner''s submitted business documents; garages only show up in car-owner search once approved.';

-- Existing is_verified boolean stays for backward-compat with earlier
-- policies but is now derived from verification_status going forward;
-- keep both in sync with a trigger so nothing that reads is_verified
-- silently goes stale.
create or replace function public.sync_garage_is_verified()
returns trigger language plpgsql as $$
begin
  new.is_verified := (new.verification_status = 'approved');
  return new;
end;
$$;

create trigger trg_sync_garage_is_verified
  before insert or update of verification_status on public.garages
  for each row execute procedure public.sync_garage_is_verified();

-- ---------------------------------------------------------------------
-- Which categories a garage offers — needed to match "nearby garages
-- that do this kind of job" rather than just "nearby garages".
-- ---------------------------------------------------------------------
create table public.garage_service_categories (
  garage_id   uuid not null references public.garages(id) on delete cascade,
  category_id uuid not null references public.service_categories(id) on delete cascade,
  primary key (garage_id, category_id)
);
comment on table public.garage_service_categories is
  'Join table: which service categories a given garage offers, used to match nearby open requests to relevant garages.';

alter table public.garage_service_categories enable row level security;

create policy "anyone can view garage categories"
  on public.garage_service_categories for select
  using (true);

create policy "garage_owner manages own categories"
  on public.garage_service_categories for all
  to authenticated
  using (public.owns_garage(garage_id) or public.is_admin())
  with check (public.owns_garage(garage_id) or public.is_admin());

-- ---------------------------------------------------------------------
-- Photos attached to a service request (damage photos, etc). Uploaded
-- client-side directly to Supabase Storage by the Flutter app; only the
-- resulting public/signed URLs are stored here.
-- ---------------------------------------------------------------------
alter table public.service_requests
  add column photo_urls jsonb not null default '[]'::jsonb;

comment on column public.service_requests.photo_urls is
  'Array of Supabase Storage URLs for photos the car owner attached, uploaded directly from the app (never proxied through this API).';

-- ---------------------------------------------------------------------
-- Helpful index for the pending-garages admin queue.
-- ---------------------------------------------------------------------
create index garages_verification_status_idx on public.garages (verification_status);

-- ---------------------------------------------------------------------
-- Tighten public discovery: car owners should only ever see garages
-- that passed admin review, but a garage_owner still needs to see (and
-- admins still need to manage) their own pending/rejected garage.
-- ---------------------------------------------------------------------
drop policy if exists "anyone can view active garages" on public.garages;

create policy "approved garages are publicly visible"
  on public.garages for select
  using (
    (is_active = true and verification_status = 'approved')
    or owner_id = auth.uid()
    or public.is_admin()
  );
