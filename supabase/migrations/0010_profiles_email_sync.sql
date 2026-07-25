-- =====================================================================
-- MIGRATION 0010: EMAIL ON PROFILES + SUPERADMIN USER MANAGEMENT
-- The admin dashboard's Users page reads straight from public.profiles
-- (no cross-schema join to auth.users), so email needs to live here too
-- for it to be visible/searchable/editable there. Kept in sync by the
-- signup trigger (below) and a trigger on auth.users for the (rare)
-- email-change case.
-- =====================================================================

alter table public.profiles
  add column email text;

create unique index profiles_email_unique_idx on public.profiles (email) where email is not null;

-- Backfill for any users created before this migration.
update public.profiles p
set email = u.email
from auth.users u
where u.id = p.id and p.email is null;

-- Extend the existing signup trigger to also capture email at insert time.
create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  insert into public.profiles (id, full_name, role, gender, date_of_birth, email)
  values (
    new.id,
    coalesce(new.raw_user_meta_data->>'full_name', 'New User'),
    coalesce((new.raw_user_meta_data->>'role')::user_role, 'car_owner'),
    new.raw_user_meta_data->>'gender',
    nullif(new.raw_user_meta_data->>'date_of_birth', '')::date,
    new.email
  );
  return new;
end;
$$;

-- Keep profiles.email in sync if an admin or the user themselves ever
-- changes it via Supabase Auth directly (rare, but cheap to handle).
create or replace function public.handle_user_email_change()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
begin
  if new.email is distinct from old.email then
    update public.profiles set email = new.email, updated_at = now() where id = new.id;
  end if;
  return new;
end;
$$;

drop trigger if exists on_auth_user_email_updated on auth.users;
create trigger on_auth_user_email_updated
  after update of email on auth.users
  for each row execute function public.handle_user_email_change();
