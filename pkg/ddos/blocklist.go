package ddos

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// blocklistCacheTTL bounds how long IsBlocked serves a cached snapshot before a
// refresh. It also caps the worst-case staleness of a newly-added block. A
// mutating call (Add/Remove/AutoBlock/SweepExpired) invalidates the cache
// immediately, so this TTL only backstops external/out-of-band DB changes.
const blocklistCacheTTL = 30 * time.Second

// blockRule is a pre-parsed blocklist entry for fast in-memory matching.
type blockRule struct {
	ipNet *net.IPNet // non-nil for CIDR entries
	ip    net.IP     // non-nil for single-IP entries
}

// blocklistCache is an atomically-swapped, pre-parsed snapshot of active rules.
type blocklistCache struct {
	rules    []blockRule
	loadedAt time.Time
}

// BlocklistStore manages the IP/CIDR blocklist backed by cpdb.
//
// IsBlocked is on the hot path (every request), so it serves matches from an
// in-memory cache (BLOCKLIST-CACHE-01) instead of scanning the whole table on
// the single-connection pool per request. The cache is refreshed at most once
// per blocklistCacheTTL and invalidated immediately on any mutation, so a fresh
// block takes effect at once while reads never touch the DB in steady state.
type BlocklistStore struct {
	db *cpdb.DB

	cacheMu sync.Mutex // serialises refreshes (single-flight)
	cache   atomic.Pointer[blocklistCache]
}

// BlocklistEntry is a row in the blocklist table.
type BlocklistEntry struct {
	CIDR         string
	Reason       string
	TTLExpiresAt *time.Time // nil = permanent
	AddedBy      string
	AddedAt      time.Time
}

// OpenBlocklistStore opens the ddos blocklist store using the provided cpdb.DB
// and runs the embedded schema migration idempotently.
func OpenBlocklistStore(db *cpdb.DB) (*BlocklistStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("ddos/blocklist: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("ddos/blocklist: migrate: %w", err)
	}
	return &BlocklistStore{db: db}, nil
}

// Close closes the database.
func (s *BlocklistStore) Close() error { return s.db.Close() }

// Add adds or replaces a blocklist entry. Pass nil ttl for a permanent block.
func (s *BlocklistStore) Add(ctx context.Context, cidr, reason, addedBy string, ttl *time.Duration) error {
	var expiresAt *string
	if ttl != nil {
		t := time.Now().UTC().Add(*ttl).Format(time.RFC3339)
		expiresAt = &t
	}
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO blocklist (cidr, reason, ttl_expires_at, added_by, added_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(cidr) DO UPDATE SET
		     reason = excluded.reason,
		     ttl_expires_at = excluded.ttl_expires_at,
		     added_by = excluded.added_by,
		     added_at = excluded.added_at`),
		cidr, reason, expiresAt, addedBy, time.Now().UTC().Format(time.RFC3339),
	)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

// Remove removes a blocklist entry by CIDR.
func (s *BlocklistStore) Remove(ctx context.Context, cidr string) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM blocklist WHERE cidr = ?`), cidr)
	if err == nil {
		s.invalidateCache()
	}
	return err
}

// List returns all current (non-expired) blocklist entries.
func (s *BlocklistStore) List(ctx context.Context) ([]BlocklistEntry, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT cidr, reason, ttl_expires_at, added_by, added_at
		 FROM blocklist
		 WHERE ttl_expires_at IS NULL OR ttl_expires_at > ?
		 ORDER BY added_at DESC`), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []BlocklistEntry
	for rows.Next() {
		var e BlocklistEntry
		var expiresStr *string
		var addedAtStr string
		if err := rows.Scan(&e.CIDR, &e.Reason, &expiresStr, &e.AddedBy, &addedAtStr); err != nil {
			return nil, err
		}
		e.AddedAt, _ = time.Parse(time.RFC3339, addedAtStr)
		if expiresStr != nil {
			t, _ := time.Parse(time.RFC3339, *expiresStr)
			e.TTLExpiresAt = &t
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// IsBlocked reports whether the given IP matches any active blocklist entry.
// It matches against the in-memory cache (BLOCKLIST-CACHE-01), refreshing it
// from the DB at most once per blocklistCacheTTL. In steady state this performs
// zero DB queries on the request hot path.
func (s *BlocklistStore) IsBlocked(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	snap := s.cache.Load()
	if snap == nil || time.Since(snap.loadedAt) >= blocklistCacheTTL {
		snap = s.refreshCache()
	}
	if snap == nil {
		return false // refresh failed and no prior snapshot — fail open (no false blocks)
	}

	for _, r := range snap.rules {
		if r.ipNet != nil {
			if r.ipNet.Contains(parsed) {
				return true
			}
			continue
		}
		if r.ip != nil && r.ip.Equal(parsed) {
			return true
		}
	}
	return false
}

// refreshCache loads active entries, pre-parses them, and atomically swaps the
// snapshot. Single-flight via cacheMu: concurrent callers that arrive during a
// refresh reuse the just-loaded snapshot rather than each hitting the DB. On
// error it returns the existing snapshot (possibly nil) without clobbering it.
func (s *BlocklistStore) refreshCache() *blocklistCache {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	// Re-check under the lock: another goroutine may have just refreshed.
	if cur := s.cache.Load(); cur != nil && time.Since(cur.loadedAt) < blocklistCacheTTL {
		return cur
	}

	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(context.Background(),
		s.db.Rebind(`SELECT cidr FROM blocklist
		 WHERE ttl_expires_at IS NULL OR ttl_expires_at > ?`), now)
	if err != nil {
		log.Printf("[ddos/blocklist] cache refresh query: %v", err)
		return s.cache.Load()
	}
	defer rows.Close()

	var rules []blockRule
	for rows.Next() {
		var cidr string
		if err := rows.Scan(&cidr); err != nil {
			continue
		}
		if _, ipNet, perr := net.ParseCIDR(cidr); perr == nil {
			rules = append(rules, blockRule{ipNet: ipNet})
			continue
		}
		if ip := net.ParseIP(cidr); ip != nil {
			rules = append(rules, blockRule{ip: ip})
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[ddos/blocklist] cache refresh rows: %v", err)
		return s.cache.Load()
	}

	snap := &blocklistCache{rules: rules, loadedAt: time.Now()}
	s.cache.Store(snap)
	return snap
}

// invalidateCache forces the next IsBlocked to reload from the DB. Called after
// any mutation so a new/removed block takes effect immediately.
func (s *BlocklistStore) invalidateCache() { s.cache.Store(nil) }

// Count returns the number of active blocklist entries.
func (s *BlocklistStore) Count(ctx context.Context) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM blocklist WHERE ttl_expires_at IS NULL OR ttl_expires_at > ?`), now,
	).Scan(&count)
	return count, err
}

// SweepExpired removes entries whose TTL has passed.
func (s *BlocklistStore) SweepExpired(ctx context.Context) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM blocklist WHERE ttl_expires_at IS NOT NULL AND ttl_expires_at <= ?`), now)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.invalidateCache()
		log.Printf("[ddos/blocklist] swept %d expired entries", n)
	}
	return nil
}

// cgnatNet is RFC 6598 shared address space (100.64.0.0/10), used by carrier-grade
// NAT and by some cloud fabrics (e.g. Fly's private network). net.IP.IsPrivate
// does not cover it, so we check it explicitly.
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// IsUnblockableIP reports whether ip must NEVER be added to the AUTOMATIC
// blocklist. A loopback / RFC1918-private / link-local / CGNAT / unspecified
// address is always a SHARED upstream — a reverse proxy, the internal fabric,
// or localhost — never a real remote client. Auto-blocking such an address
// (which happens when the edge trust header is missing and RealClientIP falls
// back to the shared proxy IP) would 403 every request routed through that
// upstream: a self-inflicted outage. Unparseable values are treated as
// unblockable too (fail safe — never block on a value we cannot reason about).
//
// It accepts a bare IP ("1.2.3.4") or a CIDR ("10.0.0.0/8").
func IsUnblockableIP(ip string) bool {
	host := strings.TrimSpace(ip)
	if p, _, err := net.ParseCIDR(host); err == nil {
		host = p.String()
	}
	p := net.ParseIP(host)
	if p == nil {
		return true
	}
	if p.IsLoopback() || p.IsPrivate() || p.IsUnspecified() ||
		p.IsLinkLocalUnicast() || p.IsLinkLocalMulticast() {
		return true
	}
	return cgnatNet.Contains(p)
}

// AutoBlock adds a temporary 1h block for the given IP (auto-detection).
//
// It REFUSES to block a private/shared/unroutable IP: automatic detection sees
// whatever RealClientIP resolves to, and if the edge trust header is absent that
// is the shared upstream proxy IP — blocking it would take down the whole plane.
func (s *BlocklistStore) AutoBlock(ctx context.Context, ip, reason string) {
	if IsUnblockableIP(ip) {
		log.Printf("[ddos/blocklist] AutoBlock REFUSED ip=%s reason=%s: private/shared/unroutable IP (blocking it would self-DoS)", ip, reason)
		return
	}
	ttl := time.Hour
	if err := s.Add(ctx, ip, reason, "system", &ttl); err != nil {
		log.Printf("[ddos/blocklist] AutoBlock ip=%s reason=%s: %v", ip, reason, err)
	}
}

// GetSetting reads a ddos_settings value.
func (s *BlocklistStore) GetSetting(ctx context.Context, key string) (string, error) {
	var val string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT value FROM ddos_settings WHERE key = ?`), key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSetting writes a ddos_settings value.
func (s *BlocklistStore) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ddos_settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`), key, value)
	return err
}
