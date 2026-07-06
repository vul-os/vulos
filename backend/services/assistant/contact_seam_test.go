package assistant

import (
	"encoding/json"
	"testing"
)

// TestContactCardWireSeam guards the assistant↔lilmail CONTACTS seam (wave 48):
// the lilContact JSON tags the assistant reads MUST match lilmail's actual
// models.Contact wire shape emitted by GET /v1/contacts/cards
// (emails[]/phones[]/note). A drift here silently blanks the field find_contact
// reads — e.g. tagging it `email` (singular) instead of `emails[]` would make
// every looked-up contact appear to have no address.
//
// The `wire` literal below is the EXACT field set lilmail's handler serializes
// (see lilmail models/contact.go: uid/name/org/title/note/emails/phones/path).
func TestContactCardWireSeam(t *testing.T) {
	const wire = `{"contacts":[` +
		`{"uid":"c-1","name":"Jane Doe","org":"Acme","title":"CTO",` +
		`"note":"met at the conf","emails":["jane@acme.io","jane.personal@x.test"],` +
		`"phones":["+1-555-0100"],"path":"/dav/addr/c-1.vcf"}` +
		`]}`
	var env struct {
		Contacts []lilContact `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(wire), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Contacts) != 1 {
		t.Fatalf("contacts envelope did not decode: %d entries", len(env.Contacts))
	}
	lc := env.Contacts[0]
	// The array fields the assistant relies on must populate from emails[]/phones[].
	if len(lc.Emails) != 2 || lc.Emails[0] != "jane@acme.io" {
		t.Fatalf("emails[] did not populate (tag mismatch with lilmail): %+v", lc.Emails)
	}
	if len(lc.Phones) != 1 || lc.Phones[0] != "+1-555-0100" {
		t.Fatalf("phones[] did not populate (tag mismatch with lilmail): %+v", lc.Phones)
	}
	if lc.Note != "met at the conf" {
		t.Fatalf("note did not populate (want lilmail `note` tag): %q", lc.Note)
	}

	// toContact must expose the PRIMARY (index-0) email/phone and the note, the
	// exact shape find_contact feeds the model.
	c := lc.toContact()
	if c.Name != "Jane Doe" || c.Email != "jane@acme.io" || c.Phone != "+1-555-0100" || c.Notes != "met at the conf" {
		t.Fatalf("toContact mapped the wrong primary fields: %+v", c)
	}

	// A card with no addresses must not fabricate one (empty, not a panic/garbage).
	empty := lilContact{Name: "No Address"}.toContact()
	if empty.Email != "" || empty.Phone != "" {
		t.Fatalf("addressless card should have empty email/phone, got %+v", empty)
	}
}
