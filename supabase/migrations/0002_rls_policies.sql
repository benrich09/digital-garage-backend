-- =====================================================================
-- MIGRATION 0002: RLS POLICIES
-- Row Level Security rules for every table. Rule of thumb used here:
--   car_owner  -> sees/edits only rows tied to their own id
--   garage/mechanic -> sees rows they are matched to (offer/booking)
--   admin      -> sees and edits everything
-- Writes to payments are intentionally left to the backend's
-- service_role key only (no client insert/update policy), since
-- payment state should never be settled directly by a mobile client.
-- =====================================================================

-- ---------------------------------------------------------------------
-- Helper functions (SECURITY DEFINER so they can read profiles/garages
-- without recursing into those tables' own RLS policies).
-- ---------------------------------------------------------------------
create or replace function public.is_admin()
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.profiles where id = auth.uid() and role = 'admin'
  );
$$;

create or replace function public.owns_garage(check_garage_id uuid)
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.garages where id = check_garage_id and owner_id = auth.uid()
  );
$$;

create or replace function public.is_mechanic(check_mechanic_id uuid)
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.mechanics where id = check_mechanic_id and profile_id = auth.uid()
  );
$$;

-- =====================================================================
-- PROFILES
-- =====================================================================
create policy "authenticated can view profiles"
  on public.profiles for select
  to authenticated
  using (true);

create policy "users update own profile"
  on public.profiles for update
  to authenticated
  using (auth.uid() = id or public.is_admin())
  with check (auth.uid() = id or public.is_admin());

create policy "admin deletes profile"
  on public.profiles for delete
  to authenticated
  using (public.is_admin());

-- =====================================================================
-- GARAGES
-- =====================================================================
create policy "anyone can view active garages"
  on public.garages for select
  using (is_active = true or owner_id = auth.uid() or public.is_admin());

create policy "garage_owner creates own garage"
  on public.garages for insert
  to authenticated
  with check (owner_id = auth.uid());

create policy "garage_owner updates own garage"
  on public.garages for update
  to authenticated
  using (owner_id = auth.uid() or public.is_admin())
  with check (owner_id = auth.uid() or public.is_admin());

create policy "garage_owner_or_admin deletes garage"
  on public.garages for delete
  to authenticated
  using (owner_id = auth.uid() or public.is_admin());

-- =====================================================================
-- MECHANICS
-- =====================================================================
create policy "authenticated can view mechanics"
  on public.mechanics for select
  to authenticated
  using (true);

create policy "garage_owner manages mechanics"
  on public.mechanics for all
  to authenticated
  using (public.owns_garage(garage_id) or public.is_admin())
  with check (public.owns_garage(garage_id) or public.is_admin());

create policy "mechanic updates own record"
  on public.mechanics for update
  to authenticated
  using (profile_id = auth.uid())
  with check (profile_id = auth.uid());

-- =====================================================================
-- MECHANIC LOCATION HISTORY
-- =====================================================================
create policy "parties view location history"
  on public.mechanic_location_history for select
  to authenticated
  using (
    exists (
      select 1 from public.bookings b
      join public.service_requests sr on sr.id = b.service_request_id
      where b.id = mechanic_location_history.booking_id
        and (sr.car_owner_id = auth.uid() or public.owns_garage(b.garage_id))
    )
    or public.is_mechanic(mechanic_location_history.mechanic_id)
    or public.is_admin()
  );

create policy "mechanic inserts own location ping"
  on public.mechanic_location_history for insert
  to authenticated
  with check (public.is_mechanic(mechanic_id));

-- =====================================================================
-- VEHICLES
-- =====================================================================
create policy "owner manages own vehicles"
  on public.vehicles for all
  to authenticated
  using (owner_id = auth.uid() or public.is_admin())
  with check (owner_id = auth.uid() or public.is_admin());

create policy "matched garage or mechanic views vehicle"
  on public.vehicles for select
  to authenticated
  using (
    exists (
      select 1 from public.service_requests sr
      join public.bookings b on b.service_request_id = sr.id
      where sr.vehicle_id = vehicles.id
        and (public.owns_garage(b.garage_id) or public.is_mechanic(b.mechanic_id))
    )
  );

-- =====================================================================
-- SERVICE CATEGORIES (public reference data)
-- =====================================================================
create policy "anyone can read service categories"
  on public.service_categories for select
  using (true);

create policy "admin manages service categories"
  on public.service_categories for all
  to authenticated
  using (public.is_admin())
  with check (public.is_admin());

-- =====================================================================
-- SERVICE REQUESTS
-- =====================================================================
create policy "car_owner views own requests"
  on public.service_requests for select
  to authenticated
  using (car_owner_id = auth.uid() or public.is_admin());

create policy "garages view open or matched requests"
  on public.service_requests for select
  to authenticated
  using (
    status = 'pending'
    or exists (
      select 1 from public.offers o
      where o.service_request_id = service_requests.id
        and (public.owns_garage(o.garage_id) or public.is_mechanic(o.mechanic_id))
    )
    or exists (
      select 1 from public.bookings b
      where b.service_request_id = service_requests.id
        and (public.owns_garage(b.garage_id) or public.is_mechanic(b.mechanic_id))
    )
  );

create policy "car_owner creates own request"
  on public.service_requests for insert
  to authenticated
  with check (car_owner_id = auth.uid());

create policy "car_owner updates own request"
  on public.service_requests for update
  to authenticated
  using (car_owner_id = auth.uid() or public.is_admin())
  with check (car_owner_id = auth.uid() or public.is_admin());

create policy "admin deletes request"
  on public.service_requests for delete
  to authenticated
  using (public.is_admin());

-- =====================================================================
-- OFFERS
-- =====================================================================
create policy "matched parties view offer"
  on public.offers for select
  to authenticated
  using (
    exists (
      select 1 from public.service_requests sr
      where sr.id = offers.service_request_id and sr.car_owner_id = auth.uid()
    )
    or public.owns_garage(garage_id)
    or public.is_mechanic(mechanic_id)
    or public.is_admin()
  );

create policy "garage_or_mechanic creates offer"
  on public.offers for insert
  to authenticated
  with check (public.owns_garage(garage_id) or public.is_mechanic(mechanic_id));

create policy "garage updates own offer"
  on public.offers for update
  to authenticated
  using (public.owns_garage(garage_id) or public.is_admin())
  with check (public.owns_garage(garage_id) or public.is_admin());

-- NOTE: this policy lets a car owner update any column on an offer tied
-- to their own request (e.g. to flip status -> accepted/rejected). For
-- tighter control, replace with a Postgres function/RPC that only
-- allows a status transition, rather than a raw table UPDATE.
create policy "car_owner responds to offer"
  on public.offers for update
  to authenticated
  using (
    exists (
      select 1 from public.service_requests sr
      where sr.id = offers.service_request_id and sr.car_owner_id = auth.uid()
    )
  )
  with check (
    exists (
      select 1 from public.service_requests sr
      where sr.id = offers.service_request_id and sr.car_owner_id = auth.uid()
    )
  );

-- =====================================================================
-- BOOKINGS
-- (Inserts are intentionally left to the backend's service_role key —
-- a booking should only be created server-side, inside the same
-- transaction that marks the winning offer 'accepted' and the rest
-- 'rejected'.)
-- =====================================================================
create policy "parties view own bookings"
  on public.bookings for select
  to authenticated
  using (
    exists (
      select 1 from public.service_requests sr
      where sr.id = bookings.service_request_id and sr.car_owner_id = auth.uid()
    )
    or public.owns_garage(garage_id)
    or public.is_mechanic(mechanic_id)
    or public.is_admin()
  );

create policy "garage_or_mechanic updates booking"
  on public.bookings for update
  to authenticated
  using (public.owns_garage(garage_id) or public.is_mechanic(mechanic_id) or public.is_admin())
  with check (public.owns_garage(garage_id) or public.is_mechanic(mechanic_id) or public.is_admin());

-- =====================================================================
-- PAYMENTS
-- (No insert/update/delete policy for clients on purpose — only the
-- Go backend, using the service_role key, should settle payments.)
-- =====================================================================
create policy "parties view own payments"
  on public.payments for select
  to authenticated
  using (
    exists (
      select 1 from public.bookings b
      join public.service_requests sr on sr.id = b.service_request_id
      where b.id = payments.booking_id
        and (sr.car_owner_id = auth.uid() or public.owns_garage(b.garage_id))
    )
    or public.is_admin()
  );

-- =====================================================================
-- REVIEWS
-- =====================================================================
create policy "anyone can read reviews"
  on public.reviews for select
  using (true);

create policy "car_owner creates review for own completed booking"
  on public.reviews for insert
  to authenticated
  with check (
    reviewer_id = auth.uid()
    and exists (
      select 1 from public.bookings b
      join public.service_requests sr on sr.id = b.service_request_id
      where b.id = reviews.booking_id and sr.car_owner_id = auth.uid()
    )
  );

create policy "admin moderates reviews"
  on public.reviews for all
  to authenticated
  using (public.is_admin())
  with check (public.is_admin());
