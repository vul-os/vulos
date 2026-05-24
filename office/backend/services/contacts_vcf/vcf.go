// Package contacts_vcf provides vCard 3.0/4.0 import and export using the
// pure-Go emersion/go-vcard library.
//
// Two top-level helpers are exposed:
//   - Import  — parses a VCF reader into a slice of Contact structs.
//   - Export  — serialises a slice of Contact structs into RFC-compliant VCF bytes.
package contacts_vcf

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	vcard "github.com/emersion/go-vcard"
)

// EmailEntry holds a labelled email address.
type EmailEntry struct {
	Label   string `json:"label"` // "home" | "work" | "other"
	Address string `json:"address"`
}

// PhoneEntry holds a labelled phone number.
type PhoneEntry struct {
	Label  string `json:"label"` // "home" | "work" | "mobile" | "other"
	Number string `json:"number"`
}

// AddressEntry holds a labelled postal address.
type AddressEntry struct {
	Label    string `json:"label"`
	Street   string `json:"street"`
	City     string `json:"city"`
	State    string `json:"state"`
	Zip      string `json:"zip"`
	Country  string `json:"country"`
}

// Contact is the canonical contact model shared between VCF import/export and
// the CardDAV store.
type Contact struct {
	UID         string         `json:"uid"`
	FirstName   string         `json:"first_name"`
	LastName    string         `json:"last_name"`
	DisplayName string         `json:"display_name"` // FN
	Emails      []EmailEntry   `json:"emails,omitempty"`
	Phones      []PhoneEntry   `json:"phones,omitempty"`
	Addresses   []AddressEntry `json:"addresses,omitempty"`
	Birthday    string         `json:"birthday,omitempty"` // YYYY-MM-DD
	Org         string         `json:"org,omitempty"`
	Title       string         `json:"title,omitempty"`
	Notes       string         `json:"notes,omitempty"`
	Starred     bool           `json:"starred,omitempty"`
	Groups      []string       `json:"groups,omitempty"`
	AvatarURL   string         `json:"avatar_url,omitempty"`
	CustomFields map[string]string `json:"custom_fields,omitempty"`
	CreatedAt   time.Time      `json:"created_at,omitempty"`
	UpdatedAt   time.Time      `json:"updated_at,omitempty"`
}

// Import parses a VCF byte stream (vCard 3.0 or 4.0) and returns all contacts
// found.  The reader is consumed entirely.  Per-card errors are collected and
// returned as a combined error if any card failed; successfully parsed cards are
// still returned.
func Import(r io.Reader) ([]Contact, error) {
	dec := vcard.NewDecoder(r)
	var contacts []Contact
	var errs []string

	for {
		card, err := dec.Decode()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}

		c := cardToContact(card)
		contacts = append(contacts, c)
	}

	if len(errs) > 0 {
		return contacts, fmt.Errorf("contacts_vcf: parse errors: %s", strings.Join(errs, "; "))
	}
	return contacts, nil
}

// Export serialises contacts to a VCF byte slice.  version must be "3.0" or
// "4.0"; any other value defaults to "4.0".
func Export(contacts []Contact, version string) ([]byte, error) {
	if version != "3.0" && version != "4.0" {
		version = "4.0"
	}

	var buf bytes.Buffer
	enc := vcard.NewEncoder(&buf)

	for _, c := range contacts {
		card := contactToCard(c, version)
		if err := enc.Encode(card); err != nil {
			return nil, fmt.Errorf("contacts_vcf: encode error for %q: %w", c.DisplayName, err)
		}
	}
	return buf.Bytes(), nil
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func cardToContact(card vcard.Card) Contact {
	c := Contact{
		UID:         card.Value(vcard.FieldUID),
		DisplayName: card.Value(vcard.FieldFormattedName),
		Org:         card.Value(vcard.FieldOrganization),
		Title:       card.Value(vcard.FieldTitle),
		Notes:       card.Value(vcard.FieldNote),
		Birthday:    card.Value(vcard.FieldBirthday),
	}

	// N field — structured name
	if nField := card.Get(vcard.FieldName); nField != nil {
		parts := nField.Value
		split := strings.SplitN(parts, ";", 5)
		if len(split) >= 1 {
			c.LastName = strings.TrimSpace(split[0])
		}
		if len(split) >= 2 {
			c.FirstName = strings.TrimSpace(split[1])
		}
	}

	if c.DisplayName == "" {
		c.DisplayName = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}

	// Emails
	for _, f := range card[vcard.FieldEmail] {
		label := paramType(f)
		c.Emails = append(c.Emails, EmailEntry{Label: label, Address: f.Value})
	}

	// Phones
	for _, f := range card[vcard.FieldTelephone] {
		label := paramType(f)
		c.Phones = append(c.Phones, PhoneEntry{Label: label, Number: f.Value})
	}

	// Addresses
	for _, f := range card[vcard.FieldAddress] {
		label := paramType(f)
		parts := strings.SplitN(f.Value, ";", 7)
		addr := AddressEntry{Label: label}
		if len(parts) > 2 {
			addr.Street = parts[2]
		}
		if len(parts) > 3 {
			addr.City = parts[3]
		}
		if len(parts) > 4 {
			addr.State = parts[4]
		}
		if len(parts) > 5 {
			addr.Zip = parts[5]
		}
		if len(parts) > 6 {
			addr.Country = parts[6]
		}
		c.Addresses = append(c.Addresses, addr)
	}

	return c
}

func contactToCard(c Contact, version string) vcard.Card {
	card := vcard.Card{}

	set := func(field, value string) {
		if value == "" {
			return
		}
		card.SetValue(field, value)
	}

	card.SetValue(vcard.FieldVersion, version)
	if c.UID != "" {
		set(vcard.FieldUID, c.UID)
	}

	// FN (required)
	fn := c.DisplayName
	if fn == "" {
		fn = strings.TrimSpace(c.FirstName + " " + c.LastName)
	}
	card.SetValue(vcard.FieldFormattedName, fn)

	// N
	card.SetValue(vcard.FieldName, c.LastName+";"+c.FirstName+";;;")

	set(vcard.FieldOrganization, c.Org)
	set(vcard.FieldTitle, c.Title)
	set(vcard.FieldNote, c.Notes)
	set(vcard.FieldBirthday, c.Birthday)

	for _, e := range c.Emails {
		f := &vcard.Field{
			Value: e.Address,
			Params: vcard.Params{vcard.ParamType: []string{e.Label}},
		}
		card[vcard.FieldEmail] = append(card[vcard.FieldEmail], f)
	}

	for _, p := range c.Phones {
		f := &vcard.Field{
			Value: p.Number,
			Params: vcard.Params{vcard.ParamType: []string{p.Label}},
		}
		card[vcard.FieldTelephone] = append(card[vcard.FieldTelephone], f)
	}

	for _, a := range c.Addresses {
		adr := ";;"+a.Street+";"+a.City+";"+a.State+";"+a.Zip+";"+a.Country
		f := &vcard.Field{
			Value: adr,
			Params: vcard.Params{vcard.ParamType: []string{a.Label}},
		}
		card[vcard.FieldAddress] = append(card[vcard.FieldAddress], f)
	}

	return card
}

// paramType extracts the first TYPE parameter value, lowercased, defaulting to
// "other".
func paramType(f *vcard.Field) string {
	if f == nil {
		return "other"
	}
	types := f.Params.Get(vcard.ParamType)
	if types == "" {
		return "other"
	}
	return strings.ToLower(strings.Split(types, ",")[0])
}
