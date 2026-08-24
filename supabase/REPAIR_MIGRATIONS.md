# Repair migration 0021 duplicate key

Remote already has version `0021` in `supabase_migrations.schema_migrations`.

## Option A — mark remaining as applied without re-running 0021

```bash
# Apply only new files via SQL Editor:
# 0022_vehicle_body_type_and_flow.sql
# 0023_realtime_location.sql

# Then tell CLI history is in sync:
supabase migration repair 0021 --status applied
supabase migration repair 0022 --status applied   # after you ran SQL
supabase migration repair 0023 --status applied   # after you ran SQL
```

## Option B — SQL Editor only (recommended)

1. Paste and run `0022_vehicle_body_type_and_flow.sql`
2. Paste and run `0023_realtime_location.sql`
3. Skip `supabase db push` for those versions or run `migration repair`

Do **not** re-run full 0021 if columns already exist.
