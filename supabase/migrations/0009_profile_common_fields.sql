-- =====================================================================
-- MIGRATION 0009: PROFILE COMMON FIELDS + SUPERADMIN PRIVILEGES
-- Adds gender (and a couple of other common fields the car_owner,
-- garage_owner, and admin/superadmin sign-up forms all collect) to
-- profiles, and extends every RLS policy that currently checks
-- role = 'admin' to also accept 'superadmin' — done in one place
-- (is_admin()) rather than touching each policy individually.
-- =====================================================================

alter table public.profiles
  add column gender text,
  add column date_of_birth date;

comment on column public.profiles.gender is
  'Optional, self-reported. NULL means not provided — never required to register.';

alter table public.profiles
  add constraint profiles_gender_check
  check (gender is null or gender in ('male', 'female', 'other', 'prefer_not_to_say'));

-- is_admin() now backs every RLS policy that previously hardcoded
-- role = 'admin' (see migration 0002) — updating it here extends admin-
-- level access to 'superadmin' everywhere at once, no per-policy edits.
create or replace function public.is_admin()
returns boolean
language sql
stable
security definer
set search_path = public
as $$
  select exists (
    select 1 from public.profiles where id = auth.uid() and role in ('admin', 'superadmin')
  );
$$;

-- Seed gender/date_of_birth at sign-up time too, same pattern as
-- full_name/role in migration 0001 — all optional, all fall back to
-- NULL rather than failing the insert if the client didn't send them.
create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  insert into public.profiles (id, full_name, role, gender, date_of_birth)
  values (
    new.id,
    coalesce(new.raw_user_meta_data->>'full_name', 'New User'),
    coalesce((new.raw_user_meta_data->>'role')::user_role, 'car_owner'),
    new.raw_user_meta_data->>'gender',
    nullif(new.raw_user_meta_data->>'date_of_birth', '')::date
  );
  return new;
end;
$$;
