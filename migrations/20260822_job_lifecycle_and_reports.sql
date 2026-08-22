-- Job lifecycle columns on bookings (safe if re-run)
alter table bookings add column if not exists started_at timestamptz;
alter table bookings add column if not exists completed_at timestamptz;
alter table bookings add column if not exists bill_amount numeric(14,2);
alter table bookings add column if not exists currency text default 'TZS';
alter table bookings add column if not exists payment_confirmed boolean default false;
alter table bookings add column if not exists customer_satisfied boolean default false;

-- Allow extended status values if bookings.status is text; if enum, cast/migrate manually.
-- Preferred: alter type ... add value — run only if using enum:
-- alter type booking_status add value if not exists 'en_route';
-- alter type booking_status add value if not exists 'awaiting_customer';
-- alter type booking_status add value if not exists 'arrived';
-- alter type booking_status add value if not exists 'awaiting_satisfaction';
-- alter type booking_status add value if not exists 'billed';
-- alter type booking_status add value if not exists 'awaiting_payment';
-- alter type booking_status add value if not exists 'paid';
-- alter type booking_status add value if not exists 'closed';

create table if not exists incident_reports (
  id uuid primary key default gen_random_uuid(),
  reporter_id uuid not null references profiles(id),
  against_user_id uuid references profiles(id),
  booking_id uuid references bookings(id),
  service_request_id uuid references service_requests(id),
  category text not null default 'other',
  description text not null,
  status text not null default 'open',
  admin_notes text,
  created_at timestamptz not null default now(),
  resolved_at timestamptz
);

create index if not exists incident_reports_reporter_idx on incident_reports(reporter_id);
create index if not exists incident_reports_status_idx on incident_reports(status);

-- request_kind on service_requests for routing
alter table service_requests add column if not exists request_kind text;
