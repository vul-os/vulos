package auth

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestValidateHandle verifies the format rules.
func TestValidateHandle(t *testing.T) {
	cases := []struct {
		handle  string
		wantErr bool
	}{
		{"alice", false},
		{"bob42", false},
		{"my_handle", false},
		{"a-b-c", false},
		{"abc", false},                   // 3-char minimum
		{"ab", true},                     // too short
		{"", true},                       // empty
		{strings.Repeat("a", 32), false}, // max length
		{strings.Repeat("a", 33), true},  // one over max
		{"-alice", true},                 // leading hyphen
		{"alice-", true},                 // trailing hyphen
		{"Alice", true},                  // uppercase (ValidateHandle is case-sensitive)
		{"alice@", true},                 // special char
		{"has space", true},              // space
	}
	for _, tc := range cases {
		err := ValidateHandle(tc.handle)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateHandle(%q) err=%v, wantErr=%v", tc.handle, err, tc.wantErr)
		}
	}
}

// TestReservedHandleBlocked verifies that reserved handles are blocked.
func TestReservedHandleBlocked(t *testing.T) {
	st := openTestStore(t)

	reserved := []string{"admin", "abuse", "support", "vulos", "root", "noreply", "postmaster"}
	for _, h := range reserved {
		result := st.CheckHandleAvailable(context.Background(), h)
		if result.Available {
			t.Errorf("reserved handle %q reported as available", h)
		}
		if result.Reason != HandleReasonReserved {
			t.Errorf("reserved handle %q got reason %q, want %q", h, result.Reason, HandleReasonReserved)
		}
	}
}

// TestSuggestionGeneration verifies that suggestions are generated for taken handles.
func TestSuggestionGeneration(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Register a handle so it becomes taken.
	_, _, err := st.Signup(ctx, "carol@vulos.org", "correctpassword123", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	result := st.CheckHandleAvailable(ctx, "carol")
	if result.Available {
		t.Fatal("expected taken, got available")
	}
	if result.Reason != HandleReasonTaken {
		t.Errorf("got reason %q, want %q", result.Reason, HandleReasonTaken)
	}
	if len(result.Suggestions) == 0 {
		t.Error("expected at least one suggestion for taken handle")
	}
	if len(result.Suggestions) > 5 {
		t.Errorf("too many suggestions: %d (max 5)", len(result.Suggestions))
	}
	// Every suggestion should pass validation.
	for _, s := range result.Suggestions {
		if err := ValidateHandle(s); err != nil {
			t.Errorf("suggestion %q failed validation: %v", s, err)
		}
	}
}

// TestHandleAvailableRaceCondition exercises concurrent availability check
// followed by signup to simulate a TOCTOU race.
// The test verifies that at most one goroutine succeeds in claiming the handle.
func TestHandleAvailableRaceCondition(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	const handle = "racetestuser"
	const n = 10

	var (
		wg       sync.WaitGroup
		succeedN int
		mu       sync.Mutex
	)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := st.Signup(ctx, handle+"@vulos.org", "password12345678", "127.0.0.1", "ua")
			if err == nil {
				mu.Lock()
				succeedN++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if succeedN != 1 {
		t.Errorf("expected exactly 1 successful signup out of %d concurrent attempts, got %d", n, succeedN)
	}
}

// TestHandleInvalidFormat verifies that malformed handles are rejected.
// Note: CheckHandleAvailable lower-cases before validation, so uppercase
// alone is not invalid (it normalises). These tests use handles that fail
// AFTER normalisation.
func TestHandleInvalidFormat(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// These handles are invalid AFTER lower-casing.
	invalidHandles := []string{
		"ab",         // too short (2 chars)
		"-bad",       // leading hyphen
		"bad-",       // trailing hyphen
		"with space", // space (invalid char)
		"has@symbol", // @ is invalid
	}
	for _, h := range invalidHandles {
		result := st.CheckHandleAvailable(ctx, h)
		if result.Available {
			t.Errorf("invalid handle %q was reported available", h)
		}
		if result.Reason != HandleReasonInvalid {
			t.Errorf("invalid handle %q got reason %q, want %q", h, result.Reason, HandleReasonInvalid)
		}
	}
}
