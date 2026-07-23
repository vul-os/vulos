package fleet_test

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/fleet"
)

// PurgeHeartbeatsBefore trims the append-only heartbeat history (retention), and
// must behave identically on both stores.
func TestPurgeHeartbeatsBefore(t *testing.T) {
	for _, sf := range storeFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			s := sf.newFunc()
			defer s.Close()
			ctx := context.Background()

			if err := s.UpsertDevice(ctx, fleet.Device{ULID: ulid1, AccountID: acct1, Health: "ok"}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			// InsertHeartbeat stamps received_at server-side (arrival time), so all
			// three land at ~now. We assert the cutoff BOUNDARY: a past cutoff keeps
			// everything, a future cutoff purges everything.
			for i := 0; i < 3; i++ {
				if _, err := s.InsertHeartbeat(ctx, fleet.Heartbeat{ULID: ulid1, Health: "ok"}); err != nil {
					t.Fatalf("heartbeat: %v", err)
				}
			}

			// A cutoff in the past removes nothing (all rows are newer).
			if n, err := s.PurgeHeartbeatsBefore(ctx, time.Now().UTC().Add(-time.Hour)); err != nil || n != 0 {
				t.Fatalf("past cutoff should purge nothing: n=%d err=%v", n, err)
			}
			if left, _ := s.RecentHeartbeats(ctx, ulid1, 100); len(left) != 3 {
				t.Fatalf("expected all 3 heartbeats to survive a past cutoff, got %d", len(left))
			}

			// A cutoff in the future removes everything.
			n, err := s.PurgeHeartbeatsBefore(ctx, time.Now().UTC().Add(time.Hour))
			if err != nil {
				t.Fatalf("purge: %v", err)
			}
			if n != 3 {
				t.Fatalf("expected all 3 heartbeats purged by a future cutoff, got %d", n)
			}
			if left, _ := s.RecentHeartbeats(ctx, ulid1, 100); len(left) != 0 {
				t.Fatalf("expected 0 heartbeats after purge, got %d", len(left))
			}
		})
	}
}
