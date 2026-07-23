-- 0001_cdn_init.sql
-- BYO-CDN config + origin-firewall state for the box owner (Settings ->
-- Network -> CDN). Single-owner scope: one box, one CDN in front of it, one
-- firewall opt-in — no account scoping (ported down from vulos.cloud's
-- multi-tenant management/pkg/cdn control plane). Pure-Go SQLite
-- (modernc.org/sqlite), no CGO.

-- cdn_config: singleton row (id always 1).
--
-- firewall_enabled defaults to 0 (disabled) — the origin firewall is
-- opt-in and OFF until the owner explicitly turns it on; see
-- backend/cmd/server/routes_cdn.go and services/cdn/firewall.go for the
-- fail-open enforcement rules.
--
-- last_ruleset / last_ruleset_at record the most recently GENERATED
-- ruleset (dry-run preview/audit trail) — not proof that anything was
-- actually installed on the box's real firewall. See firewall.go's package
-- doc: live nftables application is NOT wired in this build.
CREATE TABLE IF NOT EXISTS cdn_config (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    provider           TEXT NOT NULL DEFAULT '',   -- '' | 'cloudflare' | 'fastly' | 'bunny'
    origin_host        TEXT NOT NULL DEFAULT '',   -- e.g. "origin.example.org"
    host_header        TEXT NOT NULL DEFAULT '',   -- Host header to enforce on origin requests
    mtls_enabled       INTEGER NOT NULL DEFAULT 0, -- authenticated-origin-pulls flag (informational; not enforced by this package)
    firewall_enabled   INTEGER NOT NULL DEFAULT 0, -- opt-in; 0 = disabled (fail-open default)
    ssh_port           INTEGER NOT NULL DEFAULT 0, -- 0 => caller applies the default (22)
    extra_allow_ports  TEXT NOT NULL DEFAULT '[]', -- JSON array of ints; additional always-allow ports
    last_ruleset       TEXT NOT NULL DEFAULT '',   -- most recently generated ruleset (dry-run text), audit/UI only
    last_ruleset_at    INTEGER,                    -- unix seconds UTC; NULL = never generated
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);

-- cdn_ip_ranges: cached CDN egress IP CIDRs, refreshed periodically by
-- RangeFetcher (fetcher.go). Keyed by (provider, cidr) so a refresh can
-- upsert without duplicating rows; stale rows for a provider are pruned on
-- every successful refresh of that provider (see SetIPRanges).
CREATE TABLE IF NOT EXISTS cdn_ip_ranges (
    provider    TEXT NOT NULL,
    cidr        TEXT NOT NULL,
    fetched_at  INTEGER NOT NULL, -- unix seconds UTC
    PRIMARY KEY (provider, cidr)
);

CREATE INDEX IF NOT EXISTS ix_cdn_ip_ranges_provider ON cdn_ip_ranges (provider);
