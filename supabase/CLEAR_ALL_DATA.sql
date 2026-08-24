-- =============================================================================
-- DANGER: deletes marketplace operational data. Profiles/auth users kept.
-- Run in Supabase SQL Editor only when you intend a full wipe of jobs.
-- =============================================================================

begin;

-- Order: children first
truncate table if exists public.mechanic_location_history cascade;
truncate table if exists public.reviews cascade;
truncate table if exists public.incident_reports cascade;
truncate table if exists public.offers cascade;
truncate table if exists public.service_transactions cascade;
truncate table if exists public.commission_ledger cascade;
truncate table if exists public.provider_settlements cascade;
truncate table if exists public.bookings cascade;
truncate table if exists public.service_requests cascade;
truncate table if exists public.notifications cascade;

-- Optional: also clear garages/mechanics/vehicles (uncomment if needed)
-- truncate table if exists public.garage_services cascade;
-- truncate table if exists public.mechanics cascade;
-- truncate table if exists public.garages cascade;
-- truncate table if exists public.vehicles cascade;

commit;

-- Verify empty
select 'service_requests' as t, count(*) from service_requests
union all select 'bookings', count(*) from bookings
union all select 'offers', count(*) from offers
union all select 'reviews', count(*) from reviews
union all select 'commission_ledger', count(*) from commission_ledger;
