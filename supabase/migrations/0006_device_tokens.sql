-- =====================================================================
-- MIGRATION 0006: DEVICE PUSH TOKENS (Firebase Cloud Messaging)
-- =====================================================================

create table public.device_tokens (
  id          uuid primary key default gen_random_uuid(),
  user_id     uuid not null references public.profiles(id) on delete cascade,
  token       text not null unique,
  platform    text not null default 'android',
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now()
);
comment on table public.device_tokens is
  'FCM registration tokens per user/device. A user may have several rows (multiple devices); a token is re-upserted on every app launch so it self-heals after rotation.';

create index device_tokens_user_idx on public.device_tokens (user_id);

alter table public.device_tokens enable row level security;

-- Clients never read/write this table directly — registration goes
-- through the Go backend (which uses the postgres role and bypasses
-- RLS anyway, per the earlier note on this app's security model). RLS
-- is still enabled for defense in depth in case a client library is
-- ever pointed at this table directly.
create policy "users manage own device tokens"
  on public.device_tokens for all
  to authenticated
  using (user_id = auth.uid())
  with check (user_id = auth.uid());
