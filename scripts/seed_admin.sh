#!/usr/bin/env bash
# Creates the first admin user via Supabase's Admin API (service_role
# key required — never run this with the anon key, and never ship this
# script's output/credentials to a client). Because handle_new_user()
# (migration 0001) reads role from raw_user_meta_data on insert, setting
# user_metadata.role = "admin" here means the profile row is created
# with role='admin' automatically — no separate SQL UPDATE needed.
#
# Usage:
#   export SUPABASE_URL=https://xxxx.supabase.co
#   export SUPABASE_SERVICE_ROLE_KEY=eyJ...
#   ./scripts/seed_admin.sh admin@example.com "A-Strong-Password-123" "Admin User"

set -euo pipefail

EMAIL="${1:?usage: seed_admin.sh <email> <password> <full_name>}"
PASSWORD="${2:?usage: seed_admin.sh <email> <password> <full_name>}"
FULL_NAME="${3:-Admin User}"

: "${SUPABASE_URL:?set SUPABASE_URL first}"
: "${SUPABASE_SERVICE_ROLE_KEY:?set SUPABASE_SERVICE_ROLE_KEY first}"

curl -sS -X POST "${SUPABASE_URL}/auth/v1/admin/users" \
  -H "apikey: ${SUPABASE_SERVICE_ROLE_KEY}" \
  -H "Authorization: Bearer ${SUPABASE_SERVICE_ROLE_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"email\": \"${EMAIL}\",
    \"password\": \"${PASSWORD}\",
    \"email_confirm\": true,
    \"user_metadata\": {
      \"role\": \"admin\",
      \"full_name\": \"${FULL_NAME}\"
    }
  }" | python3 -m json.tool

echo
echo "Verify with:"
echo "  select id, role, full_name from public.profiles where full_name = '${FULL_NAME}';"
