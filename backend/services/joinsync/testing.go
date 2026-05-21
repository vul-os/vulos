package joinsync

// testing.go — test helpers compiled into every binary but intended only for
// use from tests.  Exported so sibling test packages (e.g. backend/firstboot)
// can inject mocks without importing unexported internals.

import (
	"context"
	"testing"

	"vulos/backend/services/cluster"
)

// TestBackend is the exported counterpart of the internal clusterBackend
// interface.  An external test package implements this to inject a mock.
type TestBackend interface {
	// Validate mirrors the internal validate method.
	Validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error
	// Pull mirrors the internal pull method.
	Pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(phase string, pct int)) error
}

// testBackendAdapter wraps a TestBackend to satisfy the internal clusterBackend.
type testBackendAdapter struct{ tb TestBackend }

func (a testBackendAdapter) validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error {
	return a.tb.Validate(ctx, cfg, passphrase)
}

func (a testBackendAdapter) pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(string, int)) error {
	return a.tb.Pull(ctx, cfg, passphrase, progress)
}

// SetBackendForTest replaces the active cluster backend with m for the
// duration of test t and restores the previous one at cleanup.
// The testing.T parameter makes this hard to call from non-test code.
func SetBackendForTest(t *testing.T, m TestBackend) {
	t.Helper()
	prev := backend
	backend = testBackendAdapter{m}
	t.Cleanup(func() { backend = prev })
}
