# Supabase Realtime security

## Principles
1. **Never expose service_role in apps** — only anon key + user JWT.
2. Realtime only delivers rows the client can already SELECT under RLS.
3. Prefer private channels or filtered subscriptions: `filter: 'car_owner_id=eq.${uid}'`.

## Recommended RLS for tracking tables
```sql
-- Bookings: owner or assigned provider
create policy bookings_select_parties on bookings for select to authenticated
using (
  exists (
    select 1 from service_requests sr
    where sr.id = bookings.service_request_id
      and (sr.car_owner_id = auth.uid()
           or exists (select 1 from garages g where g.id = bookings.garage_id and g.owner_id = auth.uid())
           or exists (select 1 from mechanics m where m.id = bookings.mechanic_id and m.profile_id = auth.uid()))
  )
);

-- Service requests: owner only for SELECT (providers use backend API)
create policy sr_select_owner on service_requests for select to authenticated
using (car_owner_id = auth.uid());
```

## Channel patterns (Flutter)
```dart
supabase.channel('booking-$id')
  .onPostgresChanges(
    event: PostgresChangeEvent.update,
    schema: 'public',
    table: 'bookings',
    filter: PostgresChangeFilter(
      type: PostgresChangeFilterType.eq,
      column: 'id',
      value: bookingId,
    ),
    callback: (payload) { /* update map / phase */ },
  )
  .subscribe();
```

## Backend WebSocket vs Supabase Realtime
| Path | Use |
|------|-----|
| Go WS (`status_update`, lat/lng) | Fast job lifecycle + location push |
| Supabase Realtime | Optional UI sync when app resumes; RLS-gated |

## Checklist
- [ ] RLS enabled on bookings, service_requests, reviews, incident_reports
- [ ] No broad `using (true)` SELECT on sensitive tables
- [ ] Realtime publication only for tables that need it
- [ ] Location history not publicly readable
