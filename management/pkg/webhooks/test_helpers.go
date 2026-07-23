// test_helpers.go — exported helpers used only in tests.
// These are not build-tag-guarded because they carry no sensitive state;
// they manipulate delivery rows in ways only a test would need.
package webhooks

import (
	"context"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SimulateMaxRetries advances a delivery to dead-letter status by writing
// attempt_count = len(backoffSchedule) and status = "dead".
// Used in tests to confirm dead-letter behaviour without waiting for real
// time-based backoffs.
func (s *Store) SimulateMaxRetries(ctx context.Context, deliveryID string) error {
	return s.setDeliveryStatus(ctx, deliveryID, "dead", len(backoffSchedule), time.Now().UTC())
}

// OpenForTest opens a webhook store with SSRF URL validation disabled.
// Use this in tests that register subscriptions pointing at httptest.NewServer
// (loopback) or other controlled endpoints.  Production code must use Open.
func OpenForTest(db *cpdb.DB) (*Store, error) {
	s, err := Open(db)
	if err != nil {
		return nil, err
	}
	// Bypass the SSRF guard so test servers at 127.0.0.1 are accepted.
	s.validateURL = func(string) error { return nil }
	return s, nil
}

// NewTestDispatcher creates a Dispatcher with a plain, unsecured HTTP client
// (no SSRF dial-time guard).  Use only in tests where the webhook URL points
// at a controlled endpoint (e.g. httptest.NewServer).  Production code must
// use NewDispatcher, which uses ssrfSafeDialer.
func NewTestDispatcher(store *Store) *Dispatcher {
	return NewDispatcherWithClient(store, &http.Client{Timeout: 10 * time.Second})
}
