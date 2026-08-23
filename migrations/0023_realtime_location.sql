-- Realtime tracking helpers
alter table public.bookings
  add column if not exists last_lat double precision;

alter table public.bookings
  add column if not exists last_lng double precision;

comment on column public.bookings.last_lat is 'Last published provider latitude during active job';
comment on column public.bookings.last_lng is 'Last published provider longitude during active job';

-- Helper for car owner app to read mechanic point as lat/lng
create or replace function public.mechanic_current_latlng(p_mechanic_id uuid)
returns table (lat double precision, lng double precision)
language sql
stable
security definer
as $$
  select
    ST_Y(current_location::geometry) as lat,
    ST_X(current_location::geometry) as lng
  from public.mechanics
  where id = p_mechanic_id
    and current_location is not null
  limit 1;
$$;

grant execute on function public.mechanic_current_latlng(uuid) to authenticated, anon, service_role;
