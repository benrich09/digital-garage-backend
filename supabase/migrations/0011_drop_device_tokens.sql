-- =====================================================================
-- MIGRATION 0011: DROP DEVICE_TOKENS (FIREBASE REMOVED)
-- Firebase Cloud Messaging has been removed from this project entirely
-- — real-time updates are delivered via the existing WebSocket hub
-- (internal/ws), and Supabase Auth/Postgres/Storage cover everything
-- else. device_tokens (migration 0006) had no other purpose, so it's
-- dropped rather than left as inert dead weight.
-- =====================================================================

drop table if exists public.device_tokens;
