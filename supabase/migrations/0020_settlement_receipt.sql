-- Receipt image for provider commission payments (admin verifies offline transfer).
alter table public.provider_settlements
  add column if not exists receipt_url text;

comment on column public.provider_settlements.receipt_url is
  'Public or signed URL of payment receipt uploaded by the provider.';

-- Storage bucket for receipts (run in Supabase dashboard if bucket API not available here)
-- insert into storage.buckets (id, name, public) values ('settlement-receipts', 'settlement-receipts', true)
-- on conflict do nothing;
