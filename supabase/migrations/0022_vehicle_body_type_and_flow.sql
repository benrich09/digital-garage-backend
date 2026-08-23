-- Vehicle body type (chosen when registering the car, not at booking time)
alter table public.vehicles
  add column if not exists body_type text;

comment on column public.vehicles.body_type is
  'sedan | suv | pickup | hatchback | van | other — set at vehicle registration';

-- Offer / booking decline feedback
alter table public.bookings
  add column if not exists provider_feedback text;

alter table public.service_requests
  add column if not exists decline_reason text;

alter table public.service_requests
  add column if not exists suggested_time timestamptz;
