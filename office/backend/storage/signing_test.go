package storage_test

// signing_test.go — unit tests for OFFICE-40 signing data model + local storage.
// Uses a temp directory; no database required.

import (
	"os"
	"testing"
	"time"

	"vulos-office/backend/config"
	"vulos-office/backend/models"
	"vulos-office/backend/storage"
)

func newTestLocalStorage(t *testing.T) storage.Storage {
	t.Helper()
	dir, err := os.MkdirTemp("", "office-sign-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := config.Default()
	cfg.Server.DataDir = dir
	s, err := storage.New(cfg)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return s
}

// ---- Envelope CRUD ----

func TestEnvelopeCRUD(t *testing.T) {
	s := newTestLocalStorage(t)

	env := &models.Envelope{
		ID:           "env-001",
		SourceFileID: "file-abc",
		Title:        "Test Envelope",
		Status:       models.EnvelopeStatusDraft,
		OrderMode:    models.SigningOrderSequential,
		Fields: []*models.SigningField{
			{
				ID:       "f1",
				Page:     1,
				X:        100,
				Y:        200,
				W:        150,
				H:        40,
				Type:     models.FieldTypeSignature,
				Required: true,
				SignerID: "signer-01",
			},
		},
	}

	// Create
	if err := s.CreateEnvelope(env); err != nil {
		t.Fatalf("CreateEnvelope: %v", err)
	}

	// Get
	got, err := s.GetEnvelope("env-001")
	if err != nil {
		t.Fatalf("GetEnvelope: %v", err)
	}
	if got.Title != "Test Envelope" {
		t.Errorf("title mismatch: got %q", got.Title)
	}
	if len(got.Fields) != 1 {
		t.Errorf("fields count mismatch: got %d", len(got.Fields))
	}

	// List
	list, err := s.ListEnvelopes()
	if err != nil {
		t.Fatalf("ListEnvelopes: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 envelope, got %d", len(list))
	}

	// Update
	got.Status = models.EnvelopeStatusSent
	if err := s.UpdateEnvelope(got); err != nil {
		t.Fatalf("UpdateEnvelope: %v", err)
	}
	updated, _ := s.GetEnvelope("env-001")
	if updated.Status != models.EnvelopeStatusSent {
		t.Errorf("status not updated: %s", updated.Status)
	}

	// Delete
	if err := s.DeleteEnvelope("env-001"); err != nil {
		t.Fatalf("DeleteEnvelope: %v", err)
	}
	if _, err := s.GetEnvelope("env-001"); err == nil {
		t.Error("expected error after delete, got nil")
	}
}

// ---- Signer management ----

func TestSignerUpsert(t *testing.T) {
	s := newTestLocalStorage(t)

	// Seed envelope first
	env := &models.Envelope{
		ID:        "env-002",
		Title:     "Signer Test",
		Status:    models.EnvelopeStatusDraft,
		OrderMode: models.SigningOrderParallel,
	}
	_ = s.CreateEnvelope(env)

	sg := &models.Signer{
		ID:         "sg-01",
		EnvelopeID: "env-002",
		Name:       "Alice Signer",
		Email:      "alice@example.com",
		Order:      1,
		Status:     models.SignerStatusPending,
	}

	// Insert
	if err := s.UpsertSigner(sg); err != nil {
		t.Fatalf("UpsertSigner (insert): %v", err)
	}

	got, err := s.GetSigner("sg-01")
	if err != nil {
		t.Fatalf("GetSigner: %v", err)
	}
	if got.Name != "Alice Signer" {
		t.Errorf("name mismatch: %q", got.Name)
	}

	// Update
	sg.Status = models.SignerStatusSigned
	if err := s.UpsertSigner(sg); err != nil {
		t.Fatalf("UpsertSigner (update): %v", err)
	}
	got2, _ := s.GetSigner("sg-01")
	if got2.Status != models.SignerStatusSigned {
		t.Errorf("status not updated: %s", got2.Status)
	}

	// List by envelope
	signers, err := s.ListSignersByEnvelope("env-002")
	if err != nil {
		t.Fatalf("ListSignersByEnvelope: %v", err)
	}
	if len(signers) != 1 {
		t.Errorf("expected 1 signer, got %d", len(signers))
	}
}

// ---- Append-only audit log ----

func TestAuditLogAppendOnly(t *testing.T) {
	s := newTestLocalStorage(t)

	// Seed envelope
	env := &models.Envelope{ID: "env-003", Title: "Audit Test", Status: models.EnvelopeStatusDraft}
	_ = s.CreateEnvelope(env)

	ev1 := &models.AuditEvent{
		ID:         "evt-001",
		EnvelopeID: "env-003",
		Action:     models.AuditActionCreated,
		Timestamp:  time.Now().UTC(),
		Identity:   "alice@example.com",
	}
	if err := s.AppendAuditEvent(ev1); err != nil {
		t.Fatalf("AppendAuditEvent ev1: %v", err)
	}

	ev2 := &models.AuditEvent{
		ID:         "evt-002",
		EnvelopeID: "env-003",
		SignerID:   "sg-01",
		Action:     models.AuditActionSigned,
		Timestamp:  time.Now().UTC().Add(time.Second),
		Identity:   "bob@example.com",
	}
	if err := s.AppendAuditEvent(ev2); err != nil {
		t.Fatalf("AppendAuditEvent ev2: %v", err)
	}

	// Duplicate ID must be rejected (append-only)
	if err := s.AppendAuditEvent(ev1); err == nil {
		t.Error("expected error re-appending duplicate event ID, got nil")
	}

	// List returns events in chronological order
	events, err := s.ListAuditEvents("env-003")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].ID != "evt-001" {
		t.Errorf("expected evt-001 first, got %s", events[0].ID)
	}
	if events[1].ID != "evt-002" {
		t.Errorf("expected evt-002 second, got %s", events[1].ID)
	}
}

// ---- Token index ----

func TestSignerTokenIndex(t *testing.T) {
	s := newTestLocalStorage(t)

	if err := s.StoreSignerToken("tok-abc", "env-004", "sg-02"); err != nil {
		t.Fatalf("StoreSignerToken: %v", err)
	}

	envID, signerID, err := s.ResolveToken("tok-abc")
	if err != nil {
		t.Fatalf("ResolveToken: %v", err)
	}
	if envID != "env-004" || signerID != "sg-02" {
		t.Errorf("token resolved to wrong values: %s / %s", envID, signerID)
	}

	// Unknown token
	if _, _, err := s.ResolveToken("no-such-token"); err == nil {
		t.Error("expected error for missing token, got nil")
	}
}

// ---- Verify file CRUD still works (interface not broken) ----

func TestFileCRUDStillWorks(t *testing.T) {
	s := newTestLocalStorage(t)

	f := &models.File{
		ID:      "file-test-01",
		Name:    "Test Doc",
		Type:    models.FileTypeDoc,
		Content: map[string]interface{}{"ops": []interface{}{}},
	}
	if err := s.CreateFile(f); err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	got, err := s.GetFile("file-test-01")
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Name != "Test Doc" {
		t.Errorf("name mismatch: %q", got.Name)
	}
}
