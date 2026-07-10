-- =====================================================================
-- MIGRATION 0003: STORAGE BUCKETS
--
-- Two private buckets:
--   vehicle-photos    -> car-owner-uploaded vehicle/damage photos
--   garage-documents  -> garage verification docs (license, ID, etc.)
--
-- Path convention (enforced by policy, chosen by your app code):
--   vehicle-photos/{car_owner_id}/{service_request_id}/{filename}
--   garage-documents/{garage_owner_id}/{garage_id}/{filename}
--
-- File size limits are set deliberately small to protect the 1GB
-- free-tier storage quota (see chat notes on scaling risk).
-- =====================================================================

insert into storage.buckets (id, name, public, file_size_limit, allowed_mime_types)
values
  ('vehicle-photos', 'vehicle-photos', false, 5242880, array['image/jpeg', 'image/png', 'image/webp']),
  ('garage-documents', 'garage-documents', false, 10485760, array['image/jpeg', 'image/png', 'application/pdf'])
on conflict (id) do nothing;

-- ---------------------------------------------------------------------
-- VEHICLE PHOTOS
-- ---------------------------------------------------------------------
create policy "car_owner manages own vehicle photos"
  on storage.objects for all
  to authenticated
  using (
    bucket_id = 'vehicle-photos'
    and (storage.foldername(name))[1] = auth.uid()::text
  )
  with check (
    bucket_id = 'vehicle-photos'
    and (storage.foldername(name))[1] = auth.uid()::text
  );

create policy "matched garage or mechanic views vehicle photos"
  on storage.objects for select
  to authenticated
  using (
    bucket_id = 'vehicle-photos'
    and exists (
      select 1
      from public.service_requests sr
      join public.bookings b on b.service_request_id = sr.id
      where sr.car_owner_id::text = (storage.foldername(name))[1]
        and (public.owns_garage(b.garage_id) or public.is_mechanic(b.mechanic_id))
    )
  );

create policy "admin views all vehicle photos"
  on storage.objects for select
  to authenticated
  using (bucket_id = 'vehicle-photos' and public.is_admin());

-- ---------------------------------------------------------------------
-- GARAGE VERIFICATION DOCUMENTS
-- ---------------------------------------------------------------------
create policy "garage_owner manages own documents"
  on storage.objects for all
  to authenticated
  using (
    bucket_id = 'garage-documents'
    and (storage.foldername(name))[1] = auth.uid()::text
  )
  with check (
    bucket_id = 'garage-documents'
    and (storage.foldername(name))[1] = auth.uid()::text
  );

create policy "admin manages all garage documents"
  on storage.objects for all
  to authenticated
  using (bucket_id = 'garage-documents' and public.is_admin())
  with check (bucket_id = 'garage-documents' and public.is_admin());
