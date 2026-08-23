-- Preferred garage + request kind for fast booking routing
alter table service_requests
  add column if not exists request_kind text default 'mechanic_request',
  add column if not exists preferred_garage_id uuid references garages(id),
  add column if not exists preferred_service_id uuid,
  add column if not exists scheduled_at timestamptz;

create index if not exists idx_sr_kind_status on service_requests (request_kind, status);
create index if not exists idx_sr_preferred_garage on service_requests (preferred_garage_id) where preferred_garage_id is not null;

-- Bookings lifecycle columns
alter table bookings
  add column if not exists customer_satisfied boolean default false,
  add column if not exists bill_amount numeric,
  add column if not exists currency text default 'TZS',
  add column if not exists payment_confirmed boolean default false,
  add column if not exists started_at timestamptz,
  add column if not exists completed_at timestamptz;

-- Enable realtime for tracking
-- alter publication supabase_realtime add table service_requests;
-- alter publication supabase_realtime add table bookings;
