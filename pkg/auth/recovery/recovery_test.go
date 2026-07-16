package recovery_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth/recovery"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// testKEK is a 32-byte AES-256 key for tests.
var testKEK = []byte("12345678901234567890123456789012")

func openTestStore(t *testing.T) *recovery.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := recovery.Open(db, testKEK)
	if err != nil {
		db.Close()
		t.Fatalf("recovery.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// stubResetter is a test TOTPResetter.
type stubResetter struct {
	codes []string
}

func (s *stubResetter) ResetTOTP(_ context.Context, accountID string) ([]string, error) {
	if s.codes == nil {
		s.codes = []string{"CODE1111", "CODE2222", "CODE3333"}
	}
	return s.codes, nil
}

// ─── Test 1: 14-day clock enforced ────────────────────────────────────────────

func TestRecoveryWindow14Days(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	req, err := st.SubmitRequest(ctx, "acc1", "alice@example.com", "4321")
	if err != nil {
		t.Fatal(err)
	}

	// Set the review token.
	if err := st.SetManualReviewToken(ctx, req.ID, "admin-token-123"); err != nil {
		t.Fatal(err)
	}
	// Verify the review token.
	if err := st.VerifyReviewToken(ctx, req.ID, "admin-token-123"); err != nil {
		t.Fatal(err)
	}

	// Attempt to complete BEFORE the 14-day window — must fail.
	resetter := &stubResetter{}
	_, err = st.Complete(ctx, req.ID, resetter)
	if err == nil {
		t.Fatal("expected error: 14-day window not elapsed")
	}
	if err != recovery.ErrWindowNotElapsed {
		t.Errorf("expected ErrWindowNotElapsed, got: %v", err)
	}

	// Verify the request was NOT completed (status should be "reviewing").
	loaded, err := st.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "reviewing" {
		t.Errorf("expected status=reviewing, got %q", loaded.Status)
	}
}

// ─── Test 2: ID upload encrypted at rest ──────────────────────────────────────

func TestIDUploadEncryptedAtRest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	req, err := st.SubmitRequest(ctx, "acc2", "bob@example.com", "")
	if err != nil {
		t.Fatal(err)
	}

	// Write a fake "government ID" upload.
	tmpDir := t.TempDir()
	uploadPath := filepath.Join(tmpDir, "id_upload.enc")
	sensitiveData := []byte("PASSPORT: John Doe, DOB: 1990-01-01, ID: SA1234567")

	if err := st.StoreIDUpload(ctx, req.ID, sensitiveData, uploadPath); err != nil {
		t.Fatalf("StoreIDUpload: %v", err)
	}

	// Read the file from disk — it must NOT contain the plaintext.
	ciphertext, err := os.ReadFile(uploadPath)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if len(ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}
	// Confirm the sensitive data is not in the file.
	if string(ciphertext) == string(sensitiveData) {
		t.Error("file is stored in plaintext — must be encrypted")
	}
	// A simple substring check is sufficient to confirm no plaintext leakage.
	for _, keyword := range []string{"PASSPORT", "John Doe", "SA1234567"} {
		if contains(ciphertext, []byte(keyword)) {
			t.Errorf("plaintext keyword %q found in ciphertext file", keyword)
		}
	}
}

// contains checks if haystack contains needle (simple byte search).
func contains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ─── Test 3: completion mints new codes ───────────────────────────────────────

func TestCompletionMintsNewCodes(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	req, err := st.SubmitRequest(ctx, "acc3", "charlie@example.com", "5678")
	if err != nil {
		t.Fatal(err)
	}

	// Set + verify review token.
	if err := st.SetManualReviewToken(ctx, req.ID, "admin-review-token-xyz"); err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyReviewToken(ctx, req.ID, "admin-review-token-xyz"); err != nil {
		t.Fatal(err)
	}

	// Backdate the recovery_eligible_at to simulate the 14 days having passed.
	// This is a test-only helper.
	if err := st.BackdateEligibleAt(ctx, req.ID, time.Now().UTC().Add(-15*24*time.Hour)); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	resetter := &stubResetter{codes: []string{"NEWCODE1", "NEWCODE2", "NEWCODE3"}}
	newCodes, err := st.Complete(ctx, req.ID, resetter)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(newCodes) == 0 {
		t.Error("expected new recovery codes to be returned")
	}
	for _, c := range newCodes {
		if c == "" {
			t.Error("expected non-empty recovery code")
		}
	}

	// Request must be in completed state.
	loaded, err := st.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "completed" {
		t.Errorf("expected status=completed, got %q", loaded.Status)
	}
	if loaded.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

// ─── Test 4: abuse (3 attempts in 24h) blocks in pending-review ──────────────

func TestAbuseThresholdBlocks(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// First 3 submissions should succeed (well, 2 will fail with ErrAlreadyPending
	// after the first — so cancel between each to allow new ones).
	for i := 0; i < MaxAbuse24h; i++ {
		_, err := st.SubmitRequest(ctx, "acc4", "dave@example.com", "")
		if err != nil {
			// If we get an already-pending error, cancel and retry.
			if err == recovery.ErrAlreadyPending {
				// Find and cancel the open request.
				if cancelErr := st.CancelAllPending(ctx, "acc4"); cancelErr != nil {
					t.Fatalf("cancel pending: %v", cancelErr)
				}
				_, err = st.SubmitRequest(ctx, "acc4", "dave@example.com", "")
				if err != nil {
					t.Fatalf("submit attempt %d: %v", i+1, err)
				}
			} else {
				t.Fatalf("submit attempt %d: %v", i+1, err)
			}
		}
		// Cancel after each so we can submit again.
		if cancelErr := st.CancelAllPending(ctx, "acc4"); cancelErr != nil {
			t.Fatalf("cancel after attempt %d: %v", i+1, cancelErr)
		}
	}

	// 4th attempt must trigger the abuse threshold.
	_, err := st.SubmitRequest(ctx, "acc4", "dave@example.com", "")
	if err == nil {
		t.Fatal("expected ErrAbuseThreshold on 4th attempt within 24h")
	}
	if err != recovery.ErrAbuseThreshold {
		t.Errorf("expected ErrAbuseThreshold, got: %v", err)
	}
}

// MaxAbuse24h re-exports the constant for use in tests.
const MaxAbuse24h = recovery.MaxAbuse24h
