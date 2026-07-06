package main

import (
	"encoding/json"
	"testing"

	"vulos/backend/services/files"
)

// TestAssistantFilesNodeWireSeam guards the assistant↔files-service seam (wave 55):
// the OS Files service response shape (files.Node) that the assistant's read-only
// find_file / read_file tools reason over MUST keep the field set the adapter
// (toAssistantFileNode) maps — id/name/path/is_dir/size/content_type/updated_at.
//
// A drift here silently blanks a field the assistant depends on — e.g. renaming
// `content_type` would make read_file's binary refusal read every file as
// "unknown type", and dropping `is_dir` would let it try to read a folder's
// bytes. Pinning the EXACT JSON the /api/files/* handlers serialize (routes_files
// returns raw *files.Node) makes such a drift a test failure, not a silent bug.
//
// The `wire` literal below is the exact field set files.Node serializes
// (see services/files/model.go: id/owner_id/parent_id/name/is_dir/…/path/size/
// content_type/…/updated_at).
func TestAssistantFilesNodeWireSeam(t *testing.T) {
	const wire = `{"id":"01H-node","owner_id":"alice","parent_id":"01H-root",` +
		`"name":"Q3 plan.md","is_dir":false,"bucket":"alice-bkt",` +
		`"object_key":"drive/Q3 plan.md","path":"/drive/Q3 plan.md","size":4096,` +
		`"content_type":"text/markdown","current_version_id":"v1",` +
		`"created_at":"2026-07-01T09:00:00Z","updated_at":"2026-07-06T12:30:00Z"}`

	var n files.Node
	if err := json.Unmarshal([]byte(wire), &n); err != nil {
		t.Fatalf("unmarshal files.Node wire: %v", err)
	}

	// The adapter maps EXACTLY the fields the assistant's FileRef/read logic needs.
	fn := toAssistantFileNode(&n)
	if fn.ID != "01H-node" {
		t.Fatalf("id did not populate (tag drift with files.Node): %q", fn.ID)
	}
	if fn.Name != "Q3 plan.md" {
		t.Fatalf("name did not populate: %q", fn.Name)
	}
	if fn.Path != "/drive/Q3 plan.md" {
		t.Fatalf("path did not populate — find_file would show no location: %q", fn.Path)
	}
	if fn.IsDir {
		t.Fatalf("is_dir mis-decoded true for a file — read_file would refuse a real file")
	}
	if fn.Size != 4096 {
		t.Fatalf("size did not populate: %d", fn.Size)
	}
	if fn.ContentType != "text/markdown" {
		t.Fatalf("content_type did not populate — read_file's binary check would misfire: %q", fn.ContentType)
	}
	if fn.UpdatedAt.IsZero() || fn.UpdatedAt.Year() != 2026 {
		t.Fatalf("updated_at did not populate (modified date the assistant shows): %v", fn.UpdatedAt)
	}

	// A folder node must decode is_dir=true so read_file refuses it (no byte read).
	var dir files.Node
	if err := json.Unmarshal([]byte(`{"id":"d1","name":"Docs","is_dir":true,"path":"/drive/Docs"}`), &dir); err != nil {
		t.Fatalf("unmarshal folder node: %v", err)
	}
	if fd := toAssistantFileNode(&dir); !fd.IsDir {
		t.Fatalf("folder node did not map is_dir=true: %+v", fd)
	}

	// A nil node maps to the zero value (no panic) — the read path is defensive.
	if z := toAssistantFileNode(nil); z.ID != "" || z.IsDir {
		t.Fatalf("nil node should map to zero FileNode, got %+v", z)
	}
}
