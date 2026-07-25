-- ---------------------------------------------------------------------
-- 0014: Provider capability + per-user preferences.
--
-- Three small additions the Settings screens need:
--   * car_types        — which vehicles a provider can work on
--   * notifications_enabled / locale — preferences that must survive a
--     reinstall, so they live on the row rather than in device storage
-- ---------------------------------------------------------------------

alter table public.profiles
  add column if not exists car_types text[] not null default '{}',
  add column if not exists notifications_enabled boolean not null default true,
  add column if not exists locale text not null default 'en';

comment on column public.profiles.car_types is
  'Vehicle categories a garage_owner/mechanic accepts. Empty means "not stated" — treated as "all" when matching, so an unconfigured provider is not silently excluded from jobs.';

comment on column public.profiles.locale is
  'UI language: en or sw. Stored server-side so the choice follows the user to a new device.';

alter table public.profiles
  drop constraint if exists profiles_locale_check;
alter table public.profiles
  add constraint profiles_locale_check
  check (locale in ('en', 'sw'));

-- Matching by car type needs a containment lookup, which btree can't do.
create index if not exists profiles_car_types_idx
  on public.profiles using gin (car_types);

-- --- Provider service summary -----------------------------------------
-- Backs the "My services" row in Settings, which shows only two numbers.
-- A view keeps that row to one cheap read instead of pulling every
-- service and counting client-side.
create or replace view public.provider_service_summary as
select
  p.id                                            as provider_id,
  coalesce(s.active_services, 0)                  as services_offered,
  coalesce(array_length(p.car_types, 1), 0)       as car_types_count,
  p.car_types                                     as car_types
from public.profiles p
left join (
  select provider_id, count(*) as active_services
  from public.provider_services
  where is_active
  group by provider_id
) s on s.provider_id = p.id
where p.role in ('garage_owner', 'mechanic');

comment on view public.provider_service_summary is
  'Counts for the Settings "My services" row. Detail lives in provider_services.';
