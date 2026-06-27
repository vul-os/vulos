package apps

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/files"

	"github.com/vul-os/vulos-apps/appsplatform"
	"github.com/vul-os/vulos-apps/mcp"
)

// newTestFiles builds a Files service over a throwaway DB. broker/bucketFn are nil
// — the ACL-only operations exercised here (folder create / list / share) never
// touch the storage broker.
func newTestFiles(t *testing.T) *files.Service {
	t.Helper()
	svc, err := files.New(filepath.Join(t.TempDir(), "files.db"), nil, nil)
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

// TestAdapter_FilesOp drives a real Files operation THROUGH the OS adapter's
// Act/Read seam (the same seam the REST apps platform and MCP layer call),
// confirming the adapter acts as the app's installing owner and respects the
// ACL-gated Files service.
func TestAdapter_FilesOp(t *testing.T) {
	svc := newTestFiles(t)
	a := NewOSAdapter(svc, nil, nil)
	app := &appsplatform.App{ID: "app1", OwnerID: "alice"}
	ctx := context.Background()

	// Act: create a folder at the Drive root (no target).
	created, err := a.Act(ctx, app, appsplatform.ActionRequest{
		Action:  "files.folder.create",
		Payload: json.RawMessage(`{"name":"Reports"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Act folder.create: %v", err)
	}
	node, ok := created.(*files.Node)
	if !ok || node.Name != "Reports" || !node.IsDir {
		t.Fatalf("unexpected create result: %#v", created)
	}
	if node.OwnerID != "alice" {
		t.Fatalf("folder owner = %q, want alice (app acts as its owner)", node.OwnerID)
	}

	// Read: list the Drive root and confirm the folder is visible to the owner.
	out, err := a.Read(ctx, app, appsplatform.ReadRequest{Kind: "files.list"})
	if err != nil {
		t.Fatalf("Read files.list: %v", err)
	}
	m, _ := out.(map[string]any)
	nodes, _ := m["nodes"].([]*files.Node)
	if len(nodes) != 1 || nodes[0].ID != node.ID {
		t.Fatalf("list = %#v, want the created folder", m["nodes"])
	}

	// CanAccessTarget: the owner can reach the node; a stranger's nonexistent node
	// id is reported not-found.
	if allowed, exists := a.CanAccessTarget(app, node.ID); !allowed || !exists {
		t.Fatalf("CanAccessTarget(owner) = (%v,%v), want (true,true)", allowed, exists)
	}
	if allowed, exists := a.CanAccessTarget(app, "missing"); allowed || exists {
		t.Fatalf("CanAccessTarget(missing) = (%v,%v), want (false,false)", allowed, exists)
	}
}

// TestAdapter_Scopes pins the read/write scope split the platform enforces.
func TestAdapter_Scopes(t *testing.T) {
	a := NewOSAdapter(nil, nil, nil)
	for _, k := range []string{"files.list", "files.read", "apps", "system"} {
		if got := a.RequiredScope(k); got != appsplatform.ScopeAppsRead {
			t.Errorf("RequiredScope(%q) = %q, want apps:read", k, got)
		}
	}
	for _, k := range []string{"files.folder.create", "files.write", "files.move", "files.share", "files.delete"} {
		if got := a.RequiredScope(k); got != appsplatform.ScopeAppsWrite {
			t.Errorf("RequiredScope(%q) = %q, want apps:write", k, got)
		}
	}
}

// TestMCP_InitializeToolsList mounts the OS adapter behind the MCP handler and
// drives initialize → tools/list over HTTP with a vat_ app token, confirming the
// OS exposes its Act actions as MCP tools (the agent-operable surface).
func TestMCP_InitializeToolsList(t *testing.T) {
	reg := appsplatform.NewMemoryRegistry()
	created, err := reg.Create(appsplatform.CreateParams{
		Name:     "agent",
		OwnerID:  "alice",
		Products: []string{appsplatform.ProductOS},
		Scopes:   []string{appsplatform.ScopeAppsRead, appsplatform.ScopeAppsWrite},
	})
	if err != nil {
		t.Fatalf("registry.Create: %v", err)
	}

	adapter := NewOSAdapter(newTestFiles(t), nil, osTestSys)
	h, err := mcp.NewHandler(mcp.MCPConfig{Adapter: adapter, Registry: reg})
	if err != nil {
		t.Fatalf("mcp.NewHandler: %v", err)
	}

	call := func(method string, params any) mcp.Response {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+created.Token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var resp mcp.Response
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %s response: %v (body=%s)", method, err, w.Body.String())
		}
		return resp
	}

	if init := call("initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion}); init.Error != nil {
		t.Fatalf("initialize error: %+v", init.Error)
	}

	tools := call("tools/list", nil)
	if tools.Error != nil {
		t.Fatalf("tools/list error: %+v", tools.Error)
	}
	raw, _ := json.Marshal(tools.Result)
	var listed struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range listed.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"files.folder.create", "files.write", "files.move", "files.share", "files.unshare", "files.delete"} {
		if !got[want] {
			t.Errorf("tools/list missing %q (got %v)", want, listed.Tools)
		}
	}

	// resources/list should advertise the OS read kinds (Files + app/system info).
	res := call("resources/list", nil)
	if res.Error != nil {
		t.Fatalf("resources/list error: %+v", res.Error)
	}
	rraw, _ := json.Marshal(res.Result)
	if !strings.Contains(string(rraw), "files.list") || !strings.Contains(string(rraw), "vulos://os/") {
		t.Fatalf("resources/list missing OS file resources: %s", rraw)
	}
}

func osTestSys() map[string]any { return map[string]any{"product": "vulos"} }
