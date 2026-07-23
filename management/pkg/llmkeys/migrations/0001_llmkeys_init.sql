-- 0001_llmkeys_init.sql — per-user BYO LLM provider API keys (LLM-BYOK-01).
--
-- One row per (account_id, provider). The key is stored ENCRYPTED at rest
-- (AES-256-GCM under the LLM-keys KEK); the plaintext key never lands in the
-- database, logs, or any API response. GET lists providers only.
--
-- Pure-Go modernc.org/sqlite (CGO_ENABLED=0). Idempotent: CREATE … IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS llm_keys (
    account_id  TEXT NOT NULL,
    provider    TEXT NOT NULL,            -- lowercased provider id (openai, anthropic, …)
    key_enc     TEXT NOT NULL,            -- base64(nonce||ciphertext||tag)
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    PRIMARY KEY (account_id, provider)
);
