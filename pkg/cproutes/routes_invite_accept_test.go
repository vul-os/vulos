// routes_invite_accept_test.go — HTTP tests for POST /api/org/invite/accept.
package cproutes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

// newInviteAcceptTestMux builds an auth store with one user, a fleet MemStore
// with one org, and wires POST /api/org/invite/accept. Returns the mux, the
// fleet store, the user, the session token, and the org id.
func newInviteAcceptTestMux(t *testing.T, email string) (*http.ServeMux, *fleet.MemStore, *auth.User, string, string) {
	t.Helper()

	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	u, token, err := authStore.Signup(context.Background(), email, "password-1234", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	fs := fleet.NewMemStore()
	org, err := fs.CreateOrg(context.Background(), "Acme", "owner-1")
	if err != nil {
		t.Fatalf("CreateOrg: %v", err)
	}

	mux := http.NewServeMux()
	RegisterInviteAcceptRoutes(mux, fs, authStore)
	return mux, fs, u, token, org.ID
}

func postAccept(t *testing.T, mux *http.ServeMux, inviteID, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"invite_id": inviteID})
	req := httptest.NewRequest(http.MethodPost, "/api/org/invite/accept", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// TestInviteAccept_CreatesMemberAndAppliesName is the happy path: a named
// invite addressed to the session user is accepted → member row created with
// the invited role AND the invite's display name applied.
func TestInviteAccept_CreatesMemberAndAppliesName(t *testing.T) {
	mux, fs, u, token, orgID := newInviteAcceptTestMux(t, "ada@example.com")
	ctx := context.Background()

	inv, err := fs.CreateInviteWithName(ctx, orgID, "ada@example.com", "admin", "Ada Lovelace")
	if err != nil {
		t.Fatalf("CreateInviteWithName: %v", err)
	}

	rr := postAccept(t, mux, inv.ID, token)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}

	// Member row exists with the invited role.
	role, err := fs.MemberRole(ctx, orgID, u.ID)
	if err != nil {
		t.Fatalf("MemberRole: %v", err)
	}
	if role != "admin" {
		t.Fatalf("role = %q, want admin", role)
	}

	// Display name applied from the invite.
	members, _ := fs.Members(ctx, orgID)
	var name string
	for _, m := range members {
		if m.AccountID == u.ID {
			name = m.DisplayName
		}
	}
	if name != "Ada Lovelace" {
		t.Fatalf("display name = %q, want Ada Lovelace", name)
	}

	// Invite flipped to accepted.
	got, _ := fs.GetInvite(ctx, inv.ID)
	if got.State != "accepted" {
		t.Fatalf("invite state = %q, want accepted", got.State)
	}

	// Second accept of the now-accepted invite → 409 (idempotent guard at the
	// HTTP layer; the member row is unchanged).
	rr2 := postAccept(t, mux, inv.ID, token)
	if rr2.Code != http.StatusConflict {
		t.Fatalf("re-accept status = %d, want 409", rr2.Code)
	}
}

// TestInviteAccept_WrongEmail rejects an invite addressed to another email.
func TestInviteAccept_WrongEmail(t *testing.T) {
	mux, fs, _, token, orgID := newInviteAcceptTestMux(t, "ada@example.com")
	inv, _ := fs.CreateInvite(context.Background(), orgID, "someone-else@example.com", "member")

	rr := postAccept(t, mux, inv.ID, token)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// No member row should have been created.
	if _, err := fs.MemberRole(context.Background(), orgID, "any"); err == nil {
		t.Fatal("unexpected member created on wrong-email accept")
	}
}

// TestInviteAccept_Revoked rejects a revoked invite with 409.
func TestInviteAccept_Revoked(t *testing.T) {
	mux, fs, u, token, orgID := newInviteAcceptTestMux(t, "ada@example.com")
	inv, _ := fs.CreateInvite(context.Background(), orgID, "ada@example.com", "member")
	if err := fs.RevokeInvite(context.Background(), inv.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}

	rr := postAccept(t, mux, inv.ID, token)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := fs.MemberRole(context.Background(), orgID, u.ID); err == nil {
		t.Fatal("revoked invite must not create a member")
	}
}

// TestInviteAccept_Expired rejects an invite past its TTL with 410.
func TestInviteAccept_Expired(t *testing.T) {
	mux, fs, u, token, orgID := newInviteAcceptTestMux(t, "ada@example.com")
	inv, _ := fs.CreateInvite(context.Background(), orgID, "ada@example.com", "member")

	// Shrink the TTL so the just-created invite is already expired.
	orig := fleet.InviteExpiry
	fleet.InviteExpiry = -time.Second
	t.Cleanup(func() { fleet.InviteExpiry = orig })

	rr := postAccept(t, mux, inv.ID, token)
	if rr.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := fs.MemberRole(context.Background(), orgID, u.ID); err == nil {
		t.Fatal("expired invite must not create a member")
	}
}

// TestInviteAccept_UnknownInvite returns 404 for a non-existent invite id.
func TestInviteAccept_UnknownInvite(t *testing.T) {
	mux, _, _, token, _ := newInviteAcceptTestMux(t, "ada@example.com")
	rr := postAccept(t, mux, "no-such-invite", token)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestInviteAccept_Unauthenticated returns 401 when no session cookie is sent.
func TestInviteAccept_Unauthenticated(t *testing.T) {
	mux, fs, _, _, orgID := newInviteAcceptTestMux(t, "ada@example.com")
	inv, _ := fs.CreateInvite(context.Background(), orgID, "ada@example.com", "member")
	rr := postAccept(t, mux, inv.ID, "")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}
