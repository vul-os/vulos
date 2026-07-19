// shared_test_helpers_test.go — cross-file test helpers used by the moved route
// tests (devicelink, files, storage, …). Ported from the package-main
// test infrastructure in cmd/server, with the commercial billing store replaced
// by the provider-agnostic billingport seam so these tests never import a
// commercial package (see internal/archtest).
package cproutes

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
)

// openTestAuthStore opens an in-memory auth store on a per-test temp SQLite file.
func openTestAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "auth.db") + "?_pragma=journal_mode(WAL)"
	s, err := openAuthStoreForTest(dsn, []byte("test-session-secret"))
	if err != nil {
		t.Fatalf("auth.OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// signupAndSession creates a user and returns its id + session cookie.
func signupAndSession(t *testing.T, as *auth.Store, email, password string) (userID string, cookie *http.Cookie) {
	t.Helper()
	u, token, err := as.Signup(context.Background(), email, password, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup %s: %v", email, err)
	}
	return u.ID, &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

// alwaysOverQuotaResolver is a test EntitlementResolver that reports every
// managed-storage write as over quota, so the storage-quota gate (the same one
// presign/upload run) can be exercised without a commercial billing store. It
// embeds NoopResolver for every other method (unlimited self-host defaults).
type alwaysOverQuotaResolver struct{ billingport.NoopResolver }

func (alwaysOverQuotaResolver) CheckStorageQuota(context.Context, string, int64, string) error {
	return billingport.ErrQuotaExceeded
}
