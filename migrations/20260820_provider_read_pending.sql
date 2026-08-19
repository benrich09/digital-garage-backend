-- Allow mechanics and garage owners to read open service requests
-- so the Flutter Supabase fallback inbox works under RLS.
drop policy if exists "providers_read_pending_requests" on service_requests;
create policy "providers_read_pending_requests"
on service_requests
for select
to authenticated
using (
  status in ('pending', 'quoted')
  and exists (
    select 1 from profiles p
    where p.id = auth.uid()
      and p.role in ('mechanic', 'garage_owner', 'admin', 'superadmin')
  )
);

-- Ensure mechanics can update their own location
drop policy if exists "mechanics_update_own_location" on mechanics;
create policy "mechanics_update_own_location"
on mechanics
for update
to authenticated
using (profile_id = auth.uid())
with check (profile_id = auth.uid());
