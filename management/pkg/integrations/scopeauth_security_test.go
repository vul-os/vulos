package integrations

// scopeauth_security_test.go — CAPSTONE coverage for the granted-scope
// authorization helpers. These are the enforcement points the token mint uses to
// decide whether a stored grant is allowed to perform a Drive write / GCS access
// / Contacts+Calendar read / Dropbox read+write. A read-only grant must NOT be
// treated as write-capable (least privilege), so each helper is asserted for both
// the positive and the must-deny directions.

import "testing"

func TestHasDriveWriteAccess(t *testing.T) {
	// drive.file (least-privilege write) and full drive both authorize write.
	if !HasDriveWriteAccess("openid " + ScopeDriveFile) {
		t.Fatal("drive.file must authorize Drive write")
	}
	if !HasDriveWriteAccess("openid " + ScopeDrive) {
		t.Fatal("full drive must authorize Drive write")
	}
	// A READ-ONLY grant must NOT be treated as write-capable.
	if HasDriveWriteAccess("openid " + ScopeDriveReadonly) {
		t.Fatal("drive.readonly must NOT authorize Drive write (least privilege)")
	}
	if HasDriveWriteAccess("") {
		t.Fatal("empty scopes must not authorize Drive write")
	}
}

func TestHasGCSAccess(t *testing.T) {
	if !HasGCSAccess("openid " + ScopeDevstorageReadWrite) {
		t.Fatal("devstorage.read_write must authorize GCS access")
	}
	if HasGCSAccess("openid " + ScopeDrive) {
		t.Fatal("Drive scope must NOT authorize GCS access")
	}
	if HasGCSAccess("") {
		t.Fatal("empty scopes must not authorize GCS access")
	}
}

func TestHasContactsReadAccess(t *testing.T) {
	if !HasContactsReadAccess("openid " + ScopeContactsReadonly) {
		t.Fatal("contacts.readonly must authorize Contacts read")
	}
	if HasContactsReadAccess("openid " + ScopeCalendarReadonly) {
		t.Fatal("calendar scope must NOT authorize Contacts read")
	}
}

func TestHasCalendarReadAccess(t *testing.T) {
	if !HasCalendarReadAccess("openid " + ScopeCalendarReadonly) {
		t.Fatal("calendar.readonly must authorize Calendar read")
	}
	// The broad read/write calendar scope also satisfies a read check.
	if !HasCalendarReadAccess("openid https://www.googleapis.com/auth/calendar") {
		t.Fatal("full calendar scope must authorize Calendar read")
	}
	if HasCalendarReadAccess("openid " + ScopeContactsReadonly) {
		t.Fatal("contacts scope must NOT authorize Calendar read")
	}
}

func TestHasDropboxAccess(t *testing.T) {
	read := ScopeDropboxContentRead
	write := ScopeDropboxContentWrite

	if !HasDropboxReadAccess(read) {
		t.Fatal("content.read must authorize Dropbox read")
	}
	if !HasDropboxWriteAccess(write) {
		t.Fatal("content.write must authorize Dropbox write")
	}
	// A read-only Dropbox grant must NOT be write-capable.
	if HasDropboxWriteAccess(read) {
		t.Fatal("content.read must NOT authorize Dropbox write")
	}
	// DropboxScopes returns a defensive copy.
	sc := DropboxScopes()
	if len(sc) == 0 {
		t.Fatal("DropboxScopes empty")
	}
	sc[0] = "TAMPERED"
	if DropboxScopes()[0] == "TAMPERED" {
		t.Fatal("DropboxScopes returned an aliased slice, not a copy")
	}
}
