-- ---------------------------------------------------------------------
-- 0016: Give superadmin real privileges, and let admins read the
-- commission tables at all.
--
-- Three defects, all of which make the admin dashboard show less than it
-- should — or, in the last case, show far too much to the wrong people.
--
-- 1. is_admin() checks `role = 'admin'` only. Migration 0008 added the
--    superadmin role and its comment promised the helper would be
--    widened "in the next migration". That never happened, so every
--    superadmin has been failing every RLS admin check since — unable to
--    list users, see other people's garages, or read requests. The
--    dashboard grants them MORE than an admin in its server actions
--    while the database grants them LESS. This fixes the database side.
--
-- 2. Migrations 0012 and 0013 enabled RLS on the commission tables but
--    only ever wrote "own row" policies. No admin policy exists, so the
--    Debts, Settlements and Transactions pages return an empty list for
--    everyone including admins.
--
-- 3. The reporting views were created without `security_invoker`, which
--    in Postgres means they run with the *view owner's* rights and
--    therefore bypass RLS on the tables underneath. Any authenticated
--    user could read every provider's balance. That is the reverse of
--    the intended permission and the most serious of the three.
-- ---------------------------------------------------------------------

-- --- Role helpers ------------------------------------------------------
-- security definer + a pinned search_path so the lookup reads profiles
-- without recursing into that table's own RLS policies.
create or replace function public.is_admin()
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.profiles
    where id = auth.uid()
      and role in ('admin', 'superadmin')
      and is_active
  );
$$;

comment on function public.is_admin is
  'True for admin AND superadmin. Superadmin is a strict superset of admin — anything an admin may do, a superadmin may do. Also requires is_active, so a suspended admin loses access immediately rather than at token expiry.';

create or replace function public.is_superadmin()
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.profiles
    where id = auth.uid()
      and role = 'superadmin'
      and is_active
  );
$$;

comment on function public.is_superadmin is
  'Reserved for the few actions an ordinary admin must not perform: granting admin access, and deleting accounts outright.';

-- --- Admin access to the commission tables -----------------------------
-- Read-only. The Go backend uses the service role for the writes that
-- matter (verifying a settlement writes a ledger credit), so admins get
-- visibility here without a client-side path to editing money records.

drop policy if exists service_txn_select_admin on public.service_transactions;
create policy service_txn_select_admin on public.service_transactions
  for select using (public.is_admin());

drop policy if exists ledger_select_admin on public.commission_ledger;
create policy ledger_select_admin on public.commission_ledger
  for select using (public.is_admin());

drop policy if exists settlement_select_admin on public.provider_settlements;
create policy settlement_select_admin on public.provider_settlements
  for select using (public.is_admin());

-- Admins may move a settlement through its workflow (marking overdue,
-- rejecting a bad reference). Verification still goes through the Go
-- endpoint, because that has to write the ledger credit in the same
-- breath and a direct table update would clear the status without
-- clearing the debt.
drop policy if exists settlement_update_admin on public.provider_settlements;
create policy settlement_update_admin on public.provider_settlements
  for update using (public.is_admin()) with check (public.is_admin());

-- Provider price lists and payouts: admins need to see them for support
-- and dispute handling.
drop policy if exists provider_services_select_admin on public.provider_services;
create policy provider_services_select_admin on public.provider_services
  for select using (public.is_admin());

drop policy if exists payouts_select_admin on public.payouts;
create policy payouts_select_admin on public.payouts
  for select using (public.is_admin());

-- Activity log stays private to its owner even from admins — it is a
-- personal history feature, not an audit trail, and support has no
-- reason to read it. Audit needs are served by service_transactions.

-- --- Close the view bypass ---------------------------------------------
-- security_invoker makes a view respect the CALLER's RLS rather than the
-- owner's. Combined with the admin policies above, a provider now sees
-- exactly their own row and an admin sees all of them — which is what
-- these views were always meant to do.
alter view public.provider_balances            set (security_invoker = on);
alter view public.provider_earnings            set (security_invoker = on);
alter view public.provider_monthly_performance set (security_invoker = on);
alter view public.provider_service_summary     set (security_invoker = on);

-- --- Superadmin-only guardrails ----------------------------------------
-- The dashboard already enforces these in its server actions; mirroring
-- them here means a leaked anon key can't be used to self-promote.
drop policy if exists profiles_role_change_superadmin on public.profiles;
create policy profiles_role_change_superadmin on public.profiles
  for update
  using (public.is_superadmin())
  with check (public.is_superadmin());

drop policy if exists "admin deletes profile" on public.profiles;
create policy profiles_delete_superadmin on public.profiles
  for delete using (public.is_superadmin());

comment on policy profiles_delete_superadmin on public.profiles is
  'Deleting an account is irreversible and cascades. Ordinary admins suspend (is_active = false) instead.';
