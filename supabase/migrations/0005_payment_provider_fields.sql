-- =====================================================================
-- MIGRATION 0005: MOBILE MONEY PAYMENT FIELDS
-- Adds the columns needed to reconcile Flutterwave mobile money charges
-- (M-Pesa/Tigo Pesa/Airtel Money in Tanzania) against the payments table
-- created in migration 0001.
-- =====================================================================

alter table public.payments
  add column provider              text not null default 'flutterwave',
  add column provider_tx_ref       text,
  add column provider_transaction_id text,
  add column raw_webhook_payload   jsonb,
  add column initiated_at          timestamptz not null default now();

comment on column public.payments.provider_tx_ref is
  'Our own idempotency key sent to Flutterwave as tx_ref when initiating the charge; used to match the async webhook back to this row.';
comment on column public.payments.provider_transaction_id is
  'Flutterwave''s own transaction id, returned in the webhook payload, kept for reconciliation/support lookups.';

create unique index payments_provider_tx_ref_idx on public.payments (provider_tx_ref) where provider_tx_ref is not null;
