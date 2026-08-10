-- 0002_profile_secrets.sql — split per-device credentials out of the profile blob.
--
-- profiles.data held ONE JSON document mixing two different kinds of thing:
--
--   config      display_name, avatar, theme, locale, timezone, ai_provider,
--               ai_model, initiative, settings   → belongs on every box
--   credentials ai_api_key, pin_hash             → per-device, or a bearer secret
--
-- Multi-instance sync replicates whole tables. While both halves shared one
-- blob, column-level exclusion could not reach inside it, so the ENTIRE profiles
-- domain had to be refused from replication — and settings are precisely what a
-- user expects to follow them between their own machines. Separating the storage
-- is what lets the config half replicate while the credential half is excluded
-- or keyed per device, decided independently.
--
-- The credential half lives here. Nothing reads this table except the auth
-- store, and crdtsync refuses it by name.
CREATE TABLE IF NOT EXISTS profile_secrets (
    user_id TEXT PRIMARY KEY,
    data    TEXT NOT NULL
);
