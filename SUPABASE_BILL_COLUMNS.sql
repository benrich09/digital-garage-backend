alter table bookings add column if not exists bill_amount numeric;
alter table bookings add column if not exists currency text default 'TZS';
alter table bookings add column if not exists customer_satisfied boolean default false;
alter table bookings add column if not exists payment_confirmed boolean default false;

-- Optional: allow status awaiting_payment if using enum — prefer text status
-- alter table bookings alter column status type text;

