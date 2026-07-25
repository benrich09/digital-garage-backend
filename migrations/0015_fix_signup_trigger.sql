-- ---------------------------------------------------------------------
-- 0015: Repair signup. Migration 0012 rewrote handle_new_user() and, in
-- doing so, dropped two guards that 0010 had put there for good reason:
--
--   * full_name lost its coalesce. profiles.full_name is NOT NULL, so a
--     signup without that metadata key aborts.
--   * date_of_birth lost its nullif. Casting '' to date raises
--     "invalid input syntax for type date".
--
-- 0012 also began writing phone, which carries BOTH a unique index
-- (from 0001) and the E.164 check constraint 0012 itself added. A user
-- typing 0712345678 — the normal way to write a number in Tanzania —
-- fails that check.
--
-- Any one of these raises inside an AFTER INSERT trigger on auth.users,
-- which rolls the whole transaction back. The user sees "Database error
-- saving new user" and no account exists, so they cannot log in either.
--
-- The fix has two halves: normalise the inputs, and stop letting profile
-- problems destroy the account. An auth user with a slightly wrong
-- profile is recoverable; no auth user at all is not.
-- ---------------------------------------------------------------------

-- --- Phone normalisation ----------------------------------------------
-- Accepts what people actually type and returns E.164, or NULL if it
-- cannot be salvaged. NULL is deliberate: a missing phone is a prompt to
-- fill it in later, whereas a raised exception is a lost signup.
create or replace function public.normalize_phone(raw text, default_cc text default '255')
returns text
language plpgsql
immutable
as $$
declare
  digits text;
begin
  if raw is null or btrim(raw) = '' then
    return null;
  end if;

  -- Strip everything that isn't a digit or a leading plus.
  digits := regexp_replace(raw, '[^0-9+]', '', 'g');

  if digits like '+%' then
    digits := '+' || regexp_replace(substring(digits from 2), '[^0-9]', '', 'g');
  elsif digits like '00%' then
    digits := '+' || substring(digits from 3);
  elsif digits like '0%' then
    -- Local trunk prefix: 0712345678 -> +255712345678
    digits := '+' || default_cc || substring(digits from 2);
  else
    digits := '+' || default_cc || digits;
  end if;

  if digits ~ '^\+[1-9][0-9]{7,14}$' then
    return digits;
  end if;
  return null;
end;
$$;

comment on function public.normalize_phone is
  'Best-effort E.164 conversion. Returns NULL rather than raising, so a malformed number never costs a signup.';

-- --- Repaired signup trigger -------------------------------------------
create or replace function public.handle_new_user()
returns trigger
language plpgsql
security definer
set search_path = public
as $$
declare
  v_phone text;
  v_role  user_role;
begin
  v_phone := public.normalize_phone(new.raw_user_meta_data->>'phone');

  -- An unrecognised role string would abort the cast, so validate it
  -- against the enum instead of casting blind.
  begin
    v_role := coalesce((new.raw_user_meta_data->>'role')::user_role, 'car_owner');
  exception when others then
    v_role := 'car_owner';
  end;

  -- profiles.phone carries a UNIQUE index from 0001. Two people sharing
  -- a number (or a retry after a partial failure) must not block the
  -- account from existing, so drop the phone rather than the signup.
  if v_phone is not null and exists (
    select 1 from public.profiles where phone = v_phone and id <> new.id
  ) then
    v_phone := null;
  end if;

  insert into public.profiles (id, full_name, role, gender, date_of_birth, phone, email)
  values (
    new.id,
    coalesce(nullif(btrim(new.raw_user_meta_data->>'full_name'), ''), 'New User'),
    v_role,
    nullif(btrim(new.raw_user_meta_data->>'gender'), ''),
    nullif(btrim(new.raw_user_meta_data->>'date_of_birth'), '')::date,
    v_phone,
    new.email
  )
  on conflict (id) do nothing;

  return new;

-- The whole point of this block: a profile problem must never roll back
-- the auth.users insert. Without it, one bad metadata value means the
-- person has no account at all and cannot even retry with the same
-- email. With it, they get an account and a repairable profile.
exception when others then
  raise warning 'handle_new_user failed for %: %', new.id, sqlerrm;
  return new;
end;
$$;

-- --- Backfill any accounts the broken trigger left without a profile ---
-- Anyone who signed up while 0012 was live has an auth.users row and no
-- profiles row, which fails LoadProfile on every API call and trips the
-- role guard at login. Give them one.
insert into public.profiles (id, full_name, role, gender, date_of_birth, phone, email)
select
  u.id,
  coalesce(nullif(btrim(u.raw_user_meta_data->>'full_name'), ''), 'New User'),
  coalesce(
    case
      when u.raw_user_meta_data->>'role' in ('car_owner','garage_owner','mechanic','admin','superadmin')
      then (u.raw_user_meta_data->>'role')::user_role
    end,
    'car_owner'
  ),
  nullif(btrim(u.raw_user_meta_data->>'gender'), ''),
  nullif(btrim(u.raw_user_meta_data->>'date_of_birth'), '')::date,
  public.normalize_phone(u.raw_user_meta_data->>'phone'),
  u.email
from auth.users u
left join public.profiles p on p.id = u.id
where p.id is null
on conflict (id) do nothing;

-- Existing rows may hold un-normalised numbers written by 0012 before
-- the check constraint was added, or by earlier manual inserts.
update public.profiles
set phone = public.normalize_phone(phone)
where phone is not null
  and phone !~ '^\+[1-9][0-9]{7,14}$';

-- --- Relax the phone constraint ----------------------------------------
-- Keep validating the shape, but allow NULL and let the normaliser be
-- the thing that enforces the format. A CHECK that can abort signup is
-- the wrong place for input formatting.
alter table public.profiles drop constraint if exists profiles_phone_check;
alter table public.profiles
  add constraint profiles_phone_check
  check (phone is null or phone ~ '^\+[1-9][0-9]{7,14}$')
  not valid;

-- `not valid` skips the retroactive scan; new and updated rows are still
-- checked. Run `alter table public.profiles validate constraint
-- profiles_phone_check;` once you have confirmed the backfill above left
-- no stragglers.
