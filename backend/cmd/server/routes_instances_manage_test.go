package main

// routes_instances_manage_test.go — HTTP tests for the Instances panel's
// rename/remove routes.
//
// The panel called both long before either existed; the SPA catch-all answered
// 200 text/html and the UI reported success for work the box never did. These
// tests pin that the routes now exist, are admin-gated, and that a 200 means the
// registry actually changed.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/auth"
)

// newInstanceManageMux wires the real routes over a temp registry + auth store,
// seeded with one admin, one ordinary user, one peer instance and the owner row.
func newInstanceManageMux(t *testing.T) (*http.ServeMux, *multiinstance.Registry, string, string) {
	t.Helper()

	st, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin, err := st.Register("admin", "adminpw123-secure!", "Admin")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	user, err := st.Register("bob", "bobpw123-secure!", "Bob")
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	if p, ok := st.GetProfile(admin.ID); !ok || p.Role != auth.RoleAdmin {
		t.Fatalf("first registered user is not admin: %+v", p)
	}
	if p, ok := st.GetProfile(user.ID); !ok || p.Role == auth.RoleAdmin {
		t.Fatalf("second registered user must not be admin: %+v", p)
	}

	reg, err := multiinstance.Open(filepath.Join(t.TempDir(), "instances.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	for _, inst := range []multiinstance.Instance{
		{ULID: peerULID, DisplayName: "Peer", Kind: multiinstance.KindCloud, Role: multiinstance.RolePeer, Status: multiinstance.StatusOnline},
		{ULID: ownerULID, DisplayName: "This Box", Kind: multiinstance.KindDevice, Role: multiinstance.RoleOwner, Status: multiinstance.StatusOnline},
	} {
		if err := reg.Upsert(inst); err != nil {
			t.Fatalf("seed instance: %v", err)
		}
	}

	mux := http.NewServeMux()
	registerInstanceManageRoutes(mux, reg, st)
	return mux, reg, admin.ID, user.ID
}

const (
	peerULID  = "01HWZMGMT000000000000PEER"
	ownerULID = "01HWZMGMT00000000000OWNER"
)

// doManage drives a route the way the middleware would: X-User-ID is the
// VERIFIED caller (the middleware strips any client-supplied value).
func doManage(t *testing.T, mux *http.ServeMux, method, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestInstanceRename_AdminRenamesForReal(t *testing.T) {
	mux, reg, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodPatch, "/api/instances/"+peerULID+"/rename", adminID,
		`{"display_name":"Studio Box"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var out multiinstance.Instance
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if out.DisplayName != "Studio Box" {
		t.Errorf("response display_name = %q", out.DisplayName)
	}
	if stored, ok := reg.Get(peerULID); !ok || stored.DisplayName != "Studio Box" {
		t.Errorf("registry not renamed: %+v", stored)
	}
}

func TestInstanceRename_RejectsEmptyName(t *testing.T) {
	mux, reg, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodPatch, "/api/instances/"+peerULID+"/rename", adminID,
		`{"display_name":"   "}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty rename = %d, want 422", rec.Code)
	}
	if stored, _ := reg.Get(peerULID); stored.DisplayName != "Peer" {
		t.Errorf("a rejected rename still wrote: %q", stored.DisplayName)
	}
}

func TestInstanceRename_UnknownInstanceIs404(t *testing.T) {
	mux, _, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodPatch, "/api/instances/01HWZNOSUCH00000000000001/rename", adminID,
		`{"display_name":"Ghost"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("rename unknown instance = %d, want 404", rec.Code)
	}
}

func TestInstanceRemove_AdminRemovesForReal(t *testing.T) {
	mux, reg, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodDelete, "/api/instances/"+peerULID, adminID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if _, ok := reg.Get(peerULID); ok {
		t.Fatal("LIE: remove answered 200 but the instance is still in the registry")
	}
}

func TestInstanceRemove_UnknownInstanceIs404(t *testing.T) {
	mux, _, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodDelete, "/api/instances/01HWZNOSUCH00000000000002", adminID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("remove unknown instance = %d, want 404", rec.Code)
	}
}

// TestInstanceRemove_RefusesOwner — the box cannot remove itself from its own
// fleet; the UI must be told, not quietly obeyed.
func TestInstanceRemove_RefusesOwner(t *testing.T) {
	mux, reg, adminID, _ := newInstanceManageMux(t)

	rec := doManage(t, mux, http.MethodDelete, "/api/instances/"+ownerULID, adminID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("remove owner = %d, want 409", rec.Code)
	}
	if _, ok := reg.Get(ownerULID); !ok {
		t.Fatal("owner instance was removed despite the 409")
	}
}

// TestInstanceManage_RequiresAdmin — the registry is account-wide fleet state
// (it drives routing and CRDT sync peers). A signed-in NON-admin, and an
// unauthenticated caller, must not be able to rename or remove instances.
func TestInstanceManage_RequiresAdmin(t *testing.T) {
	mux, reg, _, userID := newInstanceManageMux(t)

	calls := []struct {
		method, path, body string
	}{
		{http.MethodPatch, "/api/instances/" + peerULID + "/rename", `{"display_name":"Pwned"}`},
		{http.MethodDelete, "/api/instances/" + peerULID, ""},
	}

	for _, c := range calls {
		if rec := doManage(t, mux, c.method, c.path, "", c.body); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated = %d, want 401", c.method, c.path, rec.Code)
		}
		if rec := doManage(t, mux, c.method, c.path, userID, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as non-admin = %d, want 403", c.method, c.path, rec.Code)
		}
	}

	stored, ok := reg.Get(peerULID)
	if !ok {
		t.Fatal("a refused caller removed the instance")
	}
	if stored.DisplayName != "Peer" {
		t.Fatalf("a refused caller renamed the instance: %q", stored.DisplayName)
	}
}

// TestInstanceManage_NilAuthStoreFailsClosed — degraded boot (no auth store)
// must refuse fleet mutations, never treat "no admin exists" as "everyone is".
func TestInstanceManage_NilAuthStoreFailsClosed(t *testing.T) {
	reg, err := multiinstance.Open(filepath.Join(t.TempDir(), "instances.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	if err := reg.Upsert(multiinstance.Instance{ULID: peerULID, DisplayName: "Peer", Role: multiinstance.RolePeer}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mux := http.NewServeMux()
	registerInstanceManageRoutes(mux, reg, nil)

	rec := doManage(t, mux, http.MethodDelete, "/api/instances/"+peerULID, "some-user", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remove with nil auth store = %d, want 403", rec.Code)
	}
	if _, ok := reg.Get(peerULID); !ok {
		t.Fatal("instance removed with no auth store to authorize it")
	}
}
