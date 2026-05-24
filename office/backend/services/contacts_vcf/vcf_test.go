package contacts_vcf_test

import (
	"strings"
	"testing"

	"vulos-office/backend/services/contacts_vcf"
)

const sampleVCF4 = `BEGIN:VCARD
VERSION:4.0
UID:uid-alice
FN:Alice Smith
N:Smith;Alice;;;
EMAIL;TYPE=work:alice@work.example
EMAIL;TYPE=home:alice@home.example
TEL;TYPE=mobile:+27 82 000 0001
ORG:Acme Corp
TITLE:Engineer
NOTE:Favourite colour: teal
BDAY:1990-03-15
END:VCARD
BEGIN:VCARD
VERSION:4.0
UID:uid-bob
FN:Bob Jones
N:Jones;Bob;;;
EMAIL;TYPE=work:bob@work.example
END:VCARD
`

// TestImport_ParsesMultipleCards checks that two cards are parsed correctly.
func TestImport_ParsesMultipleCards(t *testing.T) {
	contacts, err := contacts_vcf.Import(strings.NewReader(sampleVCF4))
	if err != nil {
		t.Fatalf("Import error: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts, got %d", len(contacts))
	}
	if contacts[0].DisplayName != "Alice Smith" {
		t.Errorf("expected 'Alice Smith', got %q", contacts[0].DisplayName)
	}
	if len(contacts[0].Emails) != 2 {
		t.Errorf("expected 2 email entries, got %d", len(contacts[0].Emails))
	}
}

// TestImport_FieldMapping checks specific field mapping for Alice.
func TestImport_FieldMapping(t *testing.T) {
	contacts, err := contacts_vcf.Import(strings.NewReader(sampleVCF4))
	if err != nil {
		t.Fatalf("Import error: %v", err)
	}
	alice := contacts[0]
	if alice.FirstName != "Alice" {
		t.Errorf("first name: want 'Alice', got %q", alice.FirstName)
	}
	if alice.LastName != "Smith" {
		t.Errorf("last name: want 'Smith', got %q", alice.LastName)
	}
	if alice.Org != "Acme Corp" {
		t.Errorf("org: want 'Acme Corp', got %q", alice.Org)
	}
	if alice.Title != "Engineer" {
		t.Errorf("title: want 'Engineer', got %q", alice.Title)
	}
	if alice.Birthday != "1990-03-15" {
		t.Errorf("birthday: want '1990-03-15', got %q", alice.Birthday)
	}
}

// TestRoundtrip checks that Export → Import produces equivalent contacts.
func TestRoundtrip_VCF4(t *testing.T) {
	original := []contacts_vcf.Contact{
		{
			UID:         "rt-001",
			FirstName:   "Carol",
			LastName:    "Danvers",
			DisplayName: "Carol Danvers",
			Emails:      []contacts_vcf.EmailEntry{{Label: "work", Address: "carol@avengers.org"}},
			Phones:      []contacts_vcf.PhoneEntry{{Label: "mobile", Number: "+1 555 999 8888"}},
			Org:         "Avengers",
			Notes:       "Loves flying.",
		},
	}

	data, err := contacts_vcf.Export(original, "4.0")
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}

	imported, err := contacts_vcf.Import(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("re-Import error: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("expected 1 contact after roundtrip, got %d", len(imported))
	}

	got := imported[0]
	if got.DisplayName != "Carol Danvers" {
		t.Errorf("display name: want 'Carol Danvers', got %q", got.DisplayName)
	}
	if len(got.Emails) != 1 || got.Emails[0].Address != "carol@avengers.org" {
		t.Errorf("email: want 'carol@avengers.org', got %+v", got.Emails)
	}
	if got.Org != "Avengers" {
		t.Errorf("org: want 'Avengers', got %q", got.Org)
	}
}

// TestExport_ValidVCFFormat checks that exported VCF starts with BEGIN:VCARD.
func TestExport_ValidVCFFormat(t *testing.T) {
	contacts := []contacts_vcf.Contact{
		{UID: "test-001", DisplayName: "Test User", FirstName: "Test", LastName: "User"},
	}
	data, err := contacts_vcf.Export(contacts, "3.0")
	if err != nil {
		t.Fatalf("Export error: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "BEGIN:VCARD") {
		t.Error("exported VCF missing BEGIN:VCARD")
	}
	if !strings.Contains(out, "END:VCARD") {
		t.Error("exported VCF missing END:VCARD")
	}
	if !strings.Contains(out, "VERSION:3.0") {
		t.Error("exported VCF missing VERSION:3.0")
	}
}

// TestImport_EmptyInput returns zero contacts and no error for empty input.
func TestImport_EmptyInput(t *testing.T) {
	contacts, err := contacts_vcf.Import(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error on empty input: %v", err)
	}
	if len(contacts) != 0 {
		t.Errorf("expected 0 contacts, got %d", len(contacts))
	}
}
