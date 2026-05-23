-- AIROT-08: embedding cache + vector store.
-- All statements use IF NOT EXISTS so migration is idempotent.

-- embed_cache stores embeddings keyed by SHA-256(model||input).
-- vector_blob is a little-endian packed float32 array.
CREATE TABLE IF NOT EXISTS embed_cache (
    cache_key   TEXT PRIMARY KEY,          -- hex(SHA256(model || "\x00" || input))
    model       TEXT NOT NULL,
    input_hash  TEXT NOT NULL,             -- hex(SHA256(input)) for reference
    vector_blob BLOB NOT NULL,             -- packed little-endian float32 array
    dim         INTEGER NOT NULL,          -- number of elements
    created_at  TEXT NOT NULL DEFAULT '',
    expires_at  TEXT NOT NULL DEFAULT ''   -- RFC3339; evict when now() > expires_at
);

CREATE INDEX IF NOT EXISTS idx_embed_cache_expires ON embed_cache(expires_at);
