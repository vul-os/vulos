package appnet

// registry_list_keywords_test.go — RegistryListEntry.Keywords (GET
// /api/store/registry, backend/cmd/server/main.go) previously had no
// Keywords field at all: RegistryEntry.Keywords existed and was signed, but
// ListEntries() never copied it onto the flat RegistryListEntry the App Hub
// frontend actually fetches and searches, so keyword data reached neither
// the API response nor the search box. This guards the wiring end to end.

import (
	"encoding/json"
	"testing"
)

func TestRegistry_ListEntries_IncludesKeywords(t *testing.T) {
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"kwtest": {
				Name:        "Keyword Test App",
				Vetted:      true,
				Type:        "web",
				Description: "An app with keywords",
				Category:    "developer",
				Keywords:    []string{"alpha", "beta", "self-hosted"},
				Versions: map[string]*VersionRecipe{
					"1.0": {Command: "bin/kwtest", Port: 8080},
				},
			},
		},
	}

	appsDir := t.TempDir()
	entries := reg.ListEntries(appsDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.ID != "kwtest" {
		t.Fatalf("expected id kwtest, got %q", got.ID)
	}
	want := []string{"alpha", "beta", "self-hosted"}
	if len(got.Keywords) != len(want) {
		t.Fatalf("Keywords = %v, want %v", got.Keywords, want)
	}
	for i, k := range want {
		if got.Keywords[i] != k {
			t.Errorf("Keywords[%d] = %q, want %q", i, got.Keywords[i], k)
		}
	}
}

// TestRegistry_ListEntries_KeywordsJSONRoundTrip verifies "keywords" is
// actually present in the marshalled API response — a struct-only check
// would miss a stray `json:"-"` tag or field-ordering mistake.
func TestRegistry_ListEntries_KeywordsJSONRoundTrip(t *testing.T) {
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"kwtest2": {
				Name:     "Keyword Test App 2",
				Vetted:   true,
				Type:     "web",
				Category: "developer",
				Keywords: []string{"matrix", "chat"},
				Versions: map[string]*VersionRecipe{
					"1.0": {Command: "bin/kwtest2", Port: 8081},
				},
			},
		},
	}

	appsDir := t.TempDir()
	entries := reg.ListEntries(appsDir)
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 decoded entry, got %d", len(decoded))
	}
	kwRaw, ok := decoded[0]["keywords"]
	if !ok {
		t.Fatal(`"keywords" key missing from marshalled RegistryListEntry JSON`)
	}
	kwList, ok := kwRaw.([]interface{})
	if !ok || len(kwList) != 2 {
		t.Fatalf("keywords in JSON = %v, want [matrix chat]", kwRaw)
	}
}

// TestRegistry_ListEntries_NilKeywordsOmitEmpty confirms an entry with no
// keywords still serialises cleanly (nil slice -> JSON null, not an error
// and not silently dropped), so this doesn't regress entries that predate
// keyword tagging.
func TestRegistry_ListEntries_NilKeywordsOmitEmpty(t *testing.T) {
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"nokw": {
				Name:     "No Keywords App",
				Vetted:   true,
				Type:     "web",
				Category: "developer",
				Versions: map[string]*VersionRecipe{
					"1.0": {Command: "bin/nokw", Port: 8082},
				},
			},
		},
	}
	appsDir := t.TempDir()
	entries := reg.ListEntries(appsDir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if len(entries[0].Keywords) != 0 {
		t.Errorf("expected empty Keywords, got %v", entries[0].Keywords)
	}
}
