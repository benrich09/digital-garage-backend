-- =============================================================================
-- 0021 — Commission payback / settlement receipts + transaction helpers
-- Safe to re-run.
-- =============================================================================

-- Receipt image URL for provider offline bank-transfer payback
alter table public.provider_settlements
  add column if not exists receipt_url text;

alter table public.provider_settlements
  add column if not exists admin_note text;

alter table public.provider_settlements
  add column if not exists settled_at timestamptz;

comment on column public.provider_settlements.receipt_url is
  'URL of payment receipt photo uploaded by the provider for admin approval';

-- Ensure status column exists with sensible values
do $$
begin
  alter table public.provider_settlements
    add column if not exists status text default 'pending';
exception when others then null;
end $$;

-- service_transactions: support provider confirm flow statuses
alter table public.service_transactions
  add column if not exists status text default 'awaiting_confirmation';

create index if not exists service_transactions_request_idx
  on public.service_transactions (request_id)
  where request_id is not null;

create index if not exists service_transactions_provider_idx
  on public.service_transactions (provider_id)
  where provider_id is not null;

create index if not exists provider_settlements_status_idx
  on public.provider_settlements (status);

-- Storage bucket for settlement receipts (public read of object URLs)
insert into storage.buckets (id, name, public)
values ('settlement-receipts', 'settlement-receipts', true)
on conflict (id) do update set public = excluded.public;

-- Providers can upload their own receipt under {uid}/...
drop policy if exists settlement_receipts_upload on storage.objects;
create policy settlement_receipts_upload on storage.objects
  for insert to authenticated
  with check (
    bucket_id = 'settlement-receipts'
    and (storage.foldername(name))[1] = auth.uid()::text
  );

drop policy if exists settlement_receipts_read on storage.objects;
create policy settlement_receipts_read on storage.objects
  for select to authenticated
  using (bucket_id = 'settlement-receipts');

-- Admin/service role bypasses RLS via service key; authenticated admins may read all
