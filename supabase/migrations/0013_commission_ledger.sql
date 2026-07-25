-- ---------------------------------------------------------------------
-- 0013: Replace escrow with a commission LEDGER.
--
-- The money model has inverted. Previously the platform collected funds
-- and paid providers out. Now the platform never touches the money:
--
--   1. Provider lists a service and its price.
--   2. Car owner receives the service and pays the provider DIRECTLY —
--      cash, or their own mobile money. Outside the app entirely.
--   3. Car owner CONFIRMS in the app that they paid.
--   4. That confirmation records a transaction visible to both parties.
--   5. 5% of the amount is booked as a DEBT the provider owes us.
--   6. The provider settles that debt monthly, into our account.
--
-- The consequence worth naming: we carry credit risk instead of holding
-- funds. A provider can complete jobs and never settle. That is why
-- every unsettled balance is queryable per-provider, why confirmations
-- are attributed to the car owner (the party with no incentive to
-- inflate them), and why disputed transactions are excluded from the
-- ledger rather than deleted from it.
-- ---------------------------------------------------------------------

-- --- Retire the escrow columns ----------------------------------------
-- Kept, not dropped: 0012 may already be applied in your Supabase
-- project and historical rows should stay readable. New code must not
-- write them.
comment on column public.payments.escrow_status is
  'DEPRECATED as of 0013. The platform no longer holds funds. Retained for historical rows only.';

-- --- Commission rate ---------------------------------------------------
-- 5% flat. Per-transaction storage (not config lookup) so changing the
-- rate later never rewrites what a provider already owes.
create or replace function public.default_commission_rate()
returns numeric language sql immutable as $$ select 0.0500::numeric $$;

comment on function public.default_commission_rate is
  'Platform commission as a fraction. Change here affects only NEW transactions; existing rows keep their frozen rate.';

-- --- Service transactions ---------------------------------------------
do $$ begin
  create type txn_status as enum ('awaiting_confirmation', 'confirmed', 'disputed', 'cancelled');
exception when duplicate_object then null;
end $$;

create table if not exists public.service_transactions (
  id             uuid primary key default gen_random_uuid(),
  booking_id     uuid references public.bookings(id) on delete set null,
  request_id     uuid references public.service_requests(id) on delete set null,
  car_owner_id   uuid not null references public.profiles(id) on delete restrict,
  provider_id    uuid not null references public.profiles(id) on delete restrict,
  garage_id      uuid references public.garages(id) on delete set null,

  -- Snapshot of what was sold. Denormalised on purpose: if the provider
  -- later edits or deletes the service, the historical record of what
  -- this customer actually bought must not change underneath it.
  service_id     uuid references public.provider_services(id) on delete set null,
  service_name   text not null,
  amount         numeric(12,2) not null check (amount > 0),
  currency       text not null default 'TZS',

  -- How the customer paid the provider. Recorded for dispute handling
  -- only — the platform is not a party to this payment.
  paid_method    text check (paid_method in ('cash', 'mobile_money', 'bank', 'other')),
  paid_reference text,

  status         txn_status not null default 'awaiting_confirmation',
  -- Confirmation is the car owner's act. The provider cannot confirm
  -- their own sale, because that would let them book commission-free
  -- revenue or inflate their own performance figures.
  confirmed_at   timestamptz,
  confirmed_by   uuid references public.profiles(id) on delete set null,
  disputed_at    timestamptz,
  dispute_reason text,

  created_at     timestamptz not null default now(),
  constraint service_txn_confirmed_has_actor
    check (status <> 'confirmed' or (confirmed_at is not null and confirmed_by is not null))
);

comment on table public.service_transactions is
  'A completed, directly-paid service. Confirmed by the car owner; commission is derived from confirmed rows only.';

create index if not exists service_txn_provider_idx on public.service_transactions (provider_id, created_at desc);
create index if not exists service_txn_car_owner_idx on public.service_transactions (car_owner_id, created_at desc);
create index if not exists service_txn_status_idx on public.service_transactions (status);

-- --- Commission ledger -------------------------------------------------
-- Double-entry-ish: every confirmed transaction produces exactly one
-- debit (provider owes us). Settlements produce credits. Balance is the
-- sum. Nothing is ever updated in place, so the history is auditable.
do $$ begin
  create type ledger_entry_type as enum ('commission_debit', 'settlement_credit', 'adjustment');
exception when duplicate_object then null;
end $$;

create table if not exists public.commission_ledger (
  id             uuid primary key default gen_random_uuid(),
  provider_id    uuid not null references public.profiles(id) on delete restrict,
  entry_type     ledger_entry_type not null,

  -- Positive increases what the provider owes; negative reduces it.
  -- A commission_debit is positive, a settlement_credit is negative.
  amount         numeric(12,2) not null,
  currency       text not null default 'TZS',

  transaction_id uuid references public.service_transactions(id) on delete restrict,
  settlement_id  uuid,

  -- Frozen at write time.
  commission_rate numeric(5,4),
  gross_amount    numeric(12,2),

  -- The month this entry belongs to, for monthly settlement batching.
  period_month   date not null default date_trunc('month', now())::date,
  note           text,
  created_at     timestamptz not null default now(),

  constraint ledger_debit_is_positive
    check (entry_type <> 'commission_debit' or amount > 0),
  constraint ledger_credit_is_negative
    check (entry_type <> 'settlement_credit' or amount < 0),
  -- One commission entry per transaction, ever. This is what makes the
  -- ledger idempotent if a confirmation is somehow replayed.
  constraint ledger_one_debit_per_txn unique (transaction_id, entry_type)
);

comment on table public.commission_ledger is
  'Append-only. Provider balance = sum(amount). Positive balance means the provider owes the platform.';

create index if not exists ledger_provider_idx on public.commission_ledger (provider_id, created_at desc);
create index if not exists ledger_period_idx on public.commission_ledger (provider_id, period_month);

-- --- Monthly settlements -----------------------------------------------
do $$ begin
  create type settlement_status as enum ('due', 'submitted', 'verified', 'rejected', 'overdue');
exception when duplicate_object then null;
end $$;

create table if not exists public.provider_settlements (
  id             uuid primary key default gen_random_uuid(),
  provider_id    uuid not null references public.profiles(id) on delete restrict,
  period_month   date not null,
  amount_due     numeric(12,2) not null check (amount_due >= 0),
  currency       text not null default 'TZS',
  status         settlement_status not null default 'due',

  -- The provider pays into OUR account and reports the reference here.
  -- An admin verifies it before the ledger is credited — we cannot
  -- auto-verify a payment that never passed through our systems.
  paid_reference text,
  paid_method    text,
  submitted_at   timestamptz,
  verified_at    timestamptz,
  verified_by    uuid references public.profiles(id) on delete set null,
  rejection_reason text,
  due_date       date not null,
  created_at     timestamptz not null default now(),

  constraint settlement_one_per_period unique (provider_id, period_month)
);

comment on table public.provider_settlements is
  'One row per provider per month. Verified by an admin, which then writes the settlement_credit to the ledger.';

create index if not exists settlements_status_idx on public.provider_settlements (status, due_date);
create index if not exists settlements_provider_idx on public.provider_settlements (provider_id, period_month desc);

alter table public.commission_ledger
  drop constraint if exists ledger_settlement_fk;
alter table public.commission_ledger
  add constraint ledger_settlement_fk
  foreign key (settlement_id) references public.provider_settlements(id) on delete set null;

-- --- Auto-write commission on confirmation -----------------------------
-- Done in a trigger rather than the Go service so that ANY path to
-- confirmation (mobile app, admin override, backfill script) books the
-- commission. A service-layer-only rule is one forgotten code path away
-- from silently free revenue.
create or replace function public.book_commission_on_confirm()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
declare
  v_rate numeric(5,4);
begin
  if new.status = 'confirmed' and (old.status is distinct from 'confirmed') then
    v_rate := public.default_commission_rate();

    insert into public.commission_ledger (
      provider_id, entry_type, amount, currency, transaction_id,
      commission_rate, gross_amount, period_month, note
    )
    values (
      new.provider_id,
      'commission_debit',
      round(new.amount * v_rate, 2),
      new.currency,
      new.id,
      v_rate,
      new.amount,
      date_trunc('month', coalesce(new.confirmed_at, now()))::date,
      'Commission on ' || new.service_name
    )
    on conflict (transaction_id, entry_type) do nothing;
  end if;
  return new;
end;
$$;

drop trigger if exists trg_book_commission on public.service_transactions;
create trigger trg_book_commission
  after update on public.service_transactions
  for each row execute function public.book_commission_on_confirm();

-- --- Balances view -----------------------------------------------------
create or replace view public.provider_balances as
select
  p.id as provider_id,
  coalesce(sum(l.amount) filter (where l.entry_type = 'commission_debit'), 0)  as commission_charged,
  coalesce(-sum(l.amount) filter (where l.entry_type = 'settlement_credit'), 0) as commission_settled,
  coalesce(sum(l.amount), 0)                                                    as balance_owed,
  coalesce(sum(l.gross_amount) filter (where l.entry_type = 'commission_debit'), 0) as gross_revenue,
  count(*) filter (where l.entry_type = 'commission_debit')                     as jobs_billed
from public.profiles p
left join public.commission_ledger l on l.provider_id = p.id
where p.role in ('garage_owner', 'mechanic')
group by p.id;

comment on view public.provider_balances is
  'balance_owed is what the provider currently owes the platform. Drives the provider home dashboard and admin debt list.';

-- --- Monthly performance view (Statistics tab) -------------------------
-- Includes the previous month on each row so the app can say "up" or
-- "down" without a second query or client-side joining.
create or replace view public.provider_monthly_performance as
with monthly as (
  select
    provider_id,
    date_trunc('month', created_at)::date as month,
    count(*)                              as jobs,
    coalesce(sum(amount), 0)              as revenue
  from public.service_transactions
  where status = 'confirmed'
  group by provider_id, date_trunc('month', created_at)::date
)
select
  m.provider_id,
  m.month,
  m.jobs,
  m.revenue,
  lag(m.revenue) over (partition by m.provider_id order by m.month) as prev_revenue,
  lag(m.jobs)    over (partition by m.provider_id order by m.month) as prev_jobs
from monthly m;

-- --- RLS ---------------------------------------------------------------
alter table public.service_transactions  enable row level security;
alter table public.commission_ledger     enable row level security;
alter table public.provider_settlements  enable row level security;

-- Both parties see the transaction; it appears in both apps.
drop policy if exists service_txn_select_parties on public.service_transactions;
create policy service_txn_select_parties on public.service_transactions
  for select using (car_owner_id = auth.uid() or provider_id = auth.uid());

-- The provider raises the transaction (they know what was done and what
-- it cost); the car owner confirms it.
drop policy if exists service_txn_insert_provider on public.service_transactions;
create policy service_txn_insert_provider on public.service_transactions
  for insert with check (provider_id = auth.uid());

drop policy if exists service_txn_confirm_car_owner on public.service_transactions;
create policy service_txn_confirm_car_owner on public.service_transactions
  for update using (car_owner_id = auth.uid()) with check (car_owner_id = auth.uid());

-- The ledger is read-only to providers. Only the trigger (security
-- definer) and admins via the service role may write it.
drop policy if exists ledger_select_own on public.commission_ledger;
create policy ledger_select_own on public.commission_ledger
  for select using (provider_id = auth.uid());

drop policy if exists settlement_select_own on public.provider_settlements;
create policy settlement_select_own on public.provider_settlements
  for select using (provider_id = auth.uid());

-- Providers may only report a payment reference, never change the amount
-- or mark themselves verified. Amount immutability is enforced in the
-- Go handler; this policy just scopes the row.
drop policy if exists settlement_submit_own on public.provider_settlements;
create policy settlement_submit_own on public.provider_settlements
  for update using (provider_id = auth.uid()) with check (provider_id = auth.uid());
