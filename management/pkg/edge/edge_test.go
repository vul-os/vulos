package edge_test

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/edge"
)

// storeFactory returns a fresh Store for testing. Both MemStore and SQLStore
// are exercised via the same test suite.
type storeFactory func(t *testing.T) edge.Store

func memFactory(_ *testing.T) edge.Store {
	return edge.NewMemStore()
}

func sqlFactory(t *testing.T) edge.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN("file::memory:?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("open cpdb: %v", err)
	}
	st, err := edge.Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open sql store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runSuite runs all sub-tests against a store produced by f.
func runSuite(t *testing.T, f storeFactory) {
	t.Run("UpsertGetListNode", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		n := edge.Node{ID: "iad1", Region: "us-east", OriginURL: "https://origin.example.com"}
		got, err := st.UpsertNode(ctx, n)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got.ID != "iad1" {
			t.Fatalf("got id=%q", got.ID)
		}
		if got.Drained {
			t.Fatal("should not be drained initially")
		}

		fetched, err := st.GetNode(ctx, "iad1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if fetched.Region != "us-east" {
			t.Fatalf("region mismatch")
		}

		nodes, err := st.ListNodes(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(nodes) != 1 {
			t.Fatalf("want 1 node, got %d", len(nodes))
		}
	})

	t.Run("DrainUndrain", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		_, _ = st.UpsertNode(ctx, edge.Node{ID: "fra1", Region: "eu-west", OriginURL: "https://fra.example.com"})

		if err := st.DrainNode(ctx, "fra1"); err != nil {
			t.Fatalf("drain: %v", err)
		}
		n, _ := st.GetNode(ctx, "fra1")
		if !n.Drained {
			t.Fatal("node should be drained")
		}

		if err := st.UndrainNode(ctx, "fra1"); err != nil {
			t.Fatalf("undrain: %v", err)
		}
		n, _ = st.GetNode(ctx, "fra1")
		if n.Drained {
			t.Fatal("node should be active after undrain")
		}
	})

	t.Run("DrainNodeNotFound", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()
		if err := st.DrainNode(ctx, "nonexistent"); err != edge.ErrNodeNotFound {
			t.Fatalf("want ErrNodeNotFound, got %v", err)
		}
	})

	t.Run("SetGetListDeleteDomainConfig", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		cfg := edge.DomainConfig{
			ULID:         "01H1ABCDEF",
			Domain:       "myapp.example.com",
			CacheTTLSec:  120,
			RateLimitRPS: 50,
			Enabled:      true,
		}
		got, err := st.SetDomainConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("set: %v", err)
		}
		if got.CacheTTLSec != 120 {
			t.Fatalf("ttl mismatch: %d", got.CacheTTLSec)
		}

		fetched, err := st.GetDomainConfig(ctx, cfg.ULID, cfg.Domain)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if fetched.RateLimitRPS != 50 {
			t.Fatalf("rps mismatch")
		}

		// Update
		cfg.CacheTTLSec = 600
		got2, err := st.SetDomainConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("update set: %v", err)
		}
		if got2.CacheTTLSec != 600 {
			t.Fatalf("updated ttl mismatch: %d", got2.CacheTTLSec)
		}

		list, err := st.ListDomainConfigs(ctx, cfg.ULID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("want 1, got %d", len(list))
		}

		if err := st.DeleteDomainConfig(ctx, cfg.ULID, cfg.Domain); err != nil {
			t.Fatalf("delete: %v", err)
		}
		_, err = st.GetDomainConfig(ctx, cfg.ULID, cfg.Domain)
		if err != edge.ErrDomainNotFound {
			t.Fatalf("want ErrDomainNotFound after delete, got %v", err)
		}
	})

	t.Run("BandwidthEventEmitListTotal", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		now := time.Now().UTC()
		ev := edge.BandwidthEvent{
			AccountID:   "acct-1",
			ULID:        "01H1ABCDEF",
			Domain:      "myapp.example.com",
			BytesIn:     1024,
			BytesOut:    2048,
			PeriodStart: now.Add(-time.Hour),
			PeriodEnd:   now,
		}

		got, err := st.EmitBandwidthEvent(ctx, ev)
		if err != nil {
			t.Fatalf("emit: %v", err)
		}
		if got.ID == 0 {
			t.Fatal("want non-zero ID")
		}

		list, err := st.ListBandwidthEvents(ctx, "acct-1", 10)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("want 1 event, got %d", len(list))
		}

		// Emit a second event
		ev2 := ev
		ev2.BytesIn = 512
		ev2.BytesOut = 1024
		if _, err := st.EmitBandwidthEvent(ctx, ev2); err != nil {
			t.Fatalf("emit2: %v", err)
		}

		tot, err := st.BandwidthTotal(ctx, "01H1ABCDEF", now.Add(-2*time.Hour))
		if err != nil {
			t.Fatalf("total: %v", err)
		}
		if tot.BytesIn != 1536 {
			t.Fatalf("bytes_in want 1536, got %d", tot.BytesIn)
		}
		if tot.BytesOut != 3072 {
			t.Fatalf("bytes_out want 3072, got %d", tot.BytesOut)
		}
	})

	t.Run("BandwidthTotalSinceFilter", func(t *testing.T) {
		st := f(t)
		ctx := context.Background()

		old := time.Now().UTC().Add(-48 * time.Hour)
		// Event from 2 days ago — should be excluded when since is yesterday.
		_, _ = st.EmitBandwidthEvent(ctx, edge.BandwidthEvent{
			AccountID:   "acct-2",
			ULID:        "ULID2",
			Domain:      "old.example.com",
			BytesIn:     9999,
			BytesOut:    9999,
			PeriodStart: old.Add(-time.Hour),
			PeriodEnd:   old,
		})

		// Event from now — should be included.
		now := time.Now().UTC()
		_, _ = st.EmitBandwidthEvent(ctx, edge.BandwidthEvent{
			AccountID:   "acct-2",
			ULID:        "ULID2",
			Domain:      "new.example.com",
			BytesIn:     100,
			BytesOut:    200,
			PeriodStart: now.Add(-time.Hour),
			PeriodEnd:   now,
		})

		since := now.Add(-24 * time.Hour)
		tot, err := st.BandwidthTotal(ctx, "ULID2", since)
		if err != nil {
			t.Fatalf("total: %v", err)
		}
		if tot.BytesIn != 100 {
			t.Fatalf("bytes_in want 100, got %d (since filter broken)", tot.BytesIn)
		}
	})
}

func TestMemStore(t *testing.T) {
	runSuite(t, memFactory)
}

func TestSQLStore(t *testing.T) {
	runSuite(t, sqlFactory)
}

func TestInvalidConfig(t *testing.T) {
	ctx := context.Background()
	st := edge.NewMemStore()

	_, err := st.UpsertNode(ctx, edge.Node{}) // missing ID
	if err != edge.ErrInvalidConfig {
		t.Fatalf("want ErrInvalidConfig for empty node, got %v", err)
	}

	_, err = st.SetDomainConfig(ctx, edge.DomainConfig{}) // missing ULID
	if err != edge.ErrInvalidConfig {
		t.Fatalf("want ErrInvalidConfig for empty domain cfg, got %v", err)
	}

	_, err = st.SetDomainConfig(ctx, edge.DomainConfig{ULID: "x", Domain: "x", CacheTTLSec: -1})
	if err != edge.ErrInvalidConfig {
		t.Fatalf("want ErrInvalidConfig for negative TTL, got %v", err)
	}
}
