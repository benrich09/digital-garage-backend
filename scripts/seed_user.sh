#!/usr/bin/env bash
# Creates a user with any role via Supabase's Admin API (service_role key
# required — never run this with the anon key, never ship its output to
# a client). handle_new_user() (migrations 0001, 0009) reads role/
# full_name/gender straight from user_metadata on insert, so the
# public.profiles row is created correctly with no separate SQL step.
#
# Usage:
#   export SUPABASE_URL=https://xxxx.supabase.co
#   export SUPABASE_SERVICE_ROLE_KEY=eyJ...
#   ./scripts/seed_user.sh <email> <password> <full_name> <role> [gender]
#
# <role> must be one of: superadmin | admin | garage_owner | mechanic | car_owner
# [gender] is optional: male | female | other | prefer_not_to_say
#
# Examples:
#   ./scripts/seed_user.sh admin@digitalgarage.demo 'DemoAdmin@123' 'Admin User' admin
#   ./scripts/seed_user.sh owner@digitalgarage.demo 'DemoOwner@123' 'Garage Owner' garage_owner male
#   ./scripts/seed_user.sh car@digitalgarage.demo 'DemoCar@123' 'Car Owner' car_owner

set -euo pipefail

EMAIL="${1:?usage: seed_user.sh <email> <password> <full_name> <role> [gender]}"
PASSWORD="${2:?usage: seed_user.sh <email> <password> <full_name> <role> [gender]}"
FULL_NAME="${3:-New User}"
ROLE="${4:?role required: superadmin|admin|garage_owner|mechanic|car_owner}"
GENDER="${5:-}"

case "$ROLE" in
  superadmin|admin|garage_owner|mechanic|car_owner) ;;
  *)
    echo "Invalid role '${ROLE}'. Must be one of: superadmin, admin, garage_owner, mechanic, car_owner" >&2
    exit 1
    ;;
esac

: "${SUPABASE_URL:?set SUPABASE_URL first}"
: "${SUPABASE_SERVICE_ROLE_KEY:?set SUPABASE_SERVICE_ROLE_KEY first}"

GENDER_JSON="null"
if [ -n "$GENDER" ]; then
  GENDER_JSON="\"${GENDER}\""
fi

curl -sS -X POST "${SUPABASE_URL}/auth/v1/admin/users" \
  -H "apikey: ${SUPABASE_SERVICE_ROLE_KEY}" \
  -H "Authorization: Bearer ${SUPABASE_SERVICE_ROLE_KEY}" \
  -H "Content-Type: application/json" \
  -d "{
    \"email\": \"${EMAIL}\",
    \"password\": \"${PASSWORD}\",
    \"email_confirm\": true,
    \"user_metadata\": {
      \"role\": \"${ROLE}\",
      \"full_name\": \"${FULL_NAME}\",
      \"gender\": ${GENDER_JSON}
    }
  }" | python3 -m json.tool

echo
echo "Verify with:"
echo "  select id, role, full_name, gender from public.profiles where full_name = '${FULL_NAME}';"
