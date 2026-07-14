-- =====================================================================
-- MIGRATION 0008: ADD SUPERADMIN ROLE
-- Two web-admin tiers now exist: 'admin' (day-to-day operations) and
-- 'superadmin' (everything 'admin' can do, reserved for account owners /
-- platform operators — e.g. eventually gating destructive actions or
-- other admins' access, even though today they share identical RLS
-- privileges via is_admin() in the next migration).
--
-- This is its own migration file (not combined with anything that
-- references the new value) because Postgres requires a new enum value
-- to be committed before it can be used in comparisons/casts elsewhere.
-- =====================================================================

alter type user_role add value 'superadmin';
