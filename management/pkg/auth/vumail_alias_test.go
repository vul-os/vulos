// vumail_alias_test.go — regression test for ValidateVulosResetToken alias (Fix 10).
package auth

import (
	"context"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestValidateVulosResetTokenIsAlias ensures ValidateVulosResetToken is callable
// and behaves identically to ValidateVumailResetToken.
func TestValidateVulosResetTokenIsAlias(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN("file::memory:?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("OpenSQLiteDSN: %v", err)
	}
	store, err := OpenAuthStore(db, []byte("test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	// An invalid/nonexistent token should return ErrResetExpired from both.
	_, err1 := store.ValidateVumailResetToken(ctx, "invalid-token")
	_, err2 := store.ValidateVulosResetToken(ctx, "invalid-token")
	if err1 != ErrResetExpired {
		t.Errorf("ValidateVumailResetToken: expected ErrResetExpired, got %v", err1)
	}
	if err2 != ErrResetExpired {
		t.Errorf("ValidateVulosResetToken: expected ErrResetExpired, got %v", err2)
	}
}
