-- AIROT-04: note_embeddings table for Notes app semantic search.
-- All statements use IF NOT EXISTS so migration is idempotent.

-- note_embeddings stores per-note embedding vectors produced by /api/ai/embed.
-- vector_blob is a little-endian packed float32 array (same encoding as embed_cache).
CREATE TABLE IF NOT EXISTS note_embeddings (
    note_id     TEXT NOT NULL,
    model_slug  TEXT NOT NULL,
    vector_blob BLOB NOT NULL,          -- packed little-endian float32 array
    dim         INTEGER NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (note_id, model_slug)
);
