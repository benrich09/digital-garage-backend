-- name: GetProfileRole :one
-- The core of the auth middleware's "join to profiles for role" step.
select id, role, full_name, is_active
from profiles
where id = $1;
