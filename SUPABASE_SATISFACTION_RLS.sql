-- Allow car owners to mark satisfaction on their bookings (fallback path)
-- Run in Supabase SQL editor if customer_satisfied update is blocked by RLS.

create policy if not exists "owners_update_own_booking_satisfaction"
on bookings for update
to authenticated
using (
  exists (
    select 1 from service_requests sr
    where sr.id = bookings.service_request_id
      and sr.car_owner_id = auth.uid()
  )
)
with check (
  exists (
    select 1 from service_requests sr
    where sr.id = bookings.service_request_id
      and sr.car_owner_id = auth.uid()
  )
);
