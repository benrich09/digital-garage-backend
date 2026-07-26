-- ---------------------------------------------------------------------
-- 0017: Indexes for the admin dashboard's filter/sort columns.
--
-- The admin pages are server components with correct pagination, but
-- their ORDER BY / WHERE columns were unindexed, so every list page and
-- the overview KPIs forced sequential scans that grow with table size.
-- These indexes match the exact columns each page queries (see the
-- admin repo's (dashboard) pages). All CREATE ... IF NOT EXISTS, so this
-- is safe to run repeatedly.
--
-- Run in the Supabase SQL editor, or drop into the backend's migration
-- sequence. CONCURRENTLY is intentionally omitted so it works inside a
-- migration transaction; at your current data size the brief lock is a
-- non-issue. If these tables are already large in production, run each
-- statement separately with CREATE INDEX CONCURRENTLY instead.
-- ---------------------------------------------------------------------

-- Users page: filter by role, default sort by created_at desc.
create index if not exists profiles_created_at_idx on public.profiles (created_at desc);
create index if not exists profiles_role_idx on public.profiles (role);

-- Overview + garages page: pending/approved counts by verification_status.
create index if not exists garages_verification_status_idx on public.garages (verification_status);

-- Service requests page + overview chart: sort by requested_at, filter by status.
create index if not exists service_requests_requested_at_idx on public.service_requests (requested_at desc);
create index if not exists service_requests_status_idx on public.service_requests (status);

-- Payments page: filter by status, sort by created_at.
create index if not exists payments_status_created_idx on public.payments (status, created_at desc);

-- Settlements page: sort by due_date and status.
create index if not exists provider_settlements_status_idx on public.provider_settlements (status);
create index if not exists provider_settlements_due_date_idx on public.provider_settlements (due_date);

-- Reviews page: sort by created_at desc.
create index if not exists reviews_created_at_idx on public.reviews (created_at desc);

-- Overview revenue KPIs: sum(amount) where entry_type='commission_debit'
-- within a date window — a composite index serves both predicates.
create index if not exists commission_ledger_type_created_idx
  on public.commission_ledger (entry_type, created_at desc);

-- ---------------------------------------------------------------------
-- Text search on the Users page uses ILIKE '%q%' across full_name /
-- phone / email. A btree index can't serve a leading-wildcard ILIKE; if
-- that search feels slow as the table grows, add trigram indexes:
--
--   create extension if not exists pg_trgm;
--   create index if not exists profiles_full_name_trgm
--     on public.profiles using gin (full_name gin_trgm_ops);
--   create index if not exists profiles_email_trgm
--     on public.profiles using gin (email gin_trgm_ops);
--
-- Left commented because pg_trgm + GIN is heavier to maintain than it's
-- worth until the search is actually a bottleneck.
-- ---------------------------------------------------------------------
