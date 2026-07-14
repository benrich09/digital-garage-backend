-- =====================================================================
-- MIGRATION 0007: SWITCH PAYMENT PROVIDERS TO M-PESA / SELCOM
-- Flutterwave has been removed from the app. Existing 'flutterwave'
-- rows are relabeled to 'mpesa' (best-effort — no live Flutterwave
-- charges are expected in production data at this point), the column
-- default changes, and a check constraint keeps future rows honest.
-- =====================================================================

update public.payments
  set provider = 'mpesa'
  where provider = 'flutterwave';

alter table public.payments
  alter column provider set default 'mpesa';

alter table public.payments
  add constraint payments_provider_check
  check (provider in ('mpesa', 'selcom'));
