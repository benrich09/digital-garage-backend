-- ---------------------------------------------------------------------
-- 0018: Retire the escrow / direct-payment branch.
--
-- Migration 0013 replaced escrow (platform holds funds, pays providers
-- out) with the commission ledger (car owner pays the provider directly,
-- confirms in-app, platform books 5%). Under that model NOTHING writes
-- to `payments` or `payouts` any more — they only hold historical escrow
-- rows, and the admin/app code no longer reads them (the admin
-- Transactions page already reads `service_transactions`).
--
-- This drops the dead tables, the one view built on them, and the enums
-- that only they used. The matching Go code (payment_service/handler/
-- repository, queries/payments.sql) is deleted in the same change set.
--
-- ⚠️  DESTRUCTIVE. If you have real historical escrow rows you want to
-- keep for accounting, export them first:
--   \copy (select * from public.payments) to 'payments_backup.csv' csv header
--   \copy (select * from public.payouts)  to 'payouts_backup.csv'  csv header
-- Then run this. Everything is IF EXISTS, so it's safe to re-run.
-- ---------------------------------------------------------------------

-- The only view built on payments (escrow-era earnings rollup). Its live
-- replacement is provider_balances (from 0013), which the admin already
-- uses. Drop it before the table it depends on.
drop view if exists public.provider_earnings;

-- CASCADE also removes the RLS policies on these tables and the
-- payments.payout_id foreign key. Drop payments first (it references
-- payouts), then payouts.
drop table if exists public.payments cascade;
drop table if exists public.payouts  cascade;

-- These enums were used ONLY by the two tables above. service_transactions
-- uses txn_status, provider_settlements uses settlement_status, and
-- commission_ledger uses ledger_entry_type — none of which are touched
-- here. Safe to drop the orphans.
drop type if exists public.payment_method;
drop type if exists public.payment_status;
drop type if exists public.payout_status;
drop type if exists public.escrow_status;

-- ---------------------------------------------------------------------
-- After this runs, your live money model is exactly three tables:
--   service_transactions  — one row per completed job (car owner confirms)
--   commission_ledger     — debits (5% owed) + credits (settled)
--   provider_settlements  — the monthly bill each provider pays you
-- plus the provider_balances view that rolls them up.
-- ---------------------------------------------------------------------
