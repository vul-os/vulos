package authvault

import (
	"encoding/base32"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

// ─── Proto encoding helpers for tests ────────────────────────────────────────

// appendVarint appends a base-128 varint to buf.
func appendVarint(buf []byte, v uint64) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v)|0x80)
		v >>= 7
	}
	return append(buf, byte(v))
}

// appendTag appends a field tag (fieldNumber << 3 | wireType).
func appendTag(buf []byte, field uint64, wireType uint64) []byte {
	return appendVarint(buf, field<<3|wireType)
}

// appendLenDelim appends a length-delimited field (wire type 2).
func appendLenDelim(buf []byte, field uint64, data []byte) []byte {
	buf = appendTag(buf, field, 2)
	buf = appendVarint(buf, uint64(len(data)))
	return append(buf, data...)
}

// appendString appends a string field.
func appendString(buf []byte, field uint64, s string) []byte {
	return appendLenDelim(buf, field, []byte(s))
}

// appendEnum appends a varint field (for enums).
func appendEnum(buf []byte, field uint64, v uint64) []byte {
	buf = appendTag(buf, field, 0)
	return appendVarint(buf, v)
}

// buildOtpParameters encodes one OtpParameters submessage.
// All fields are always written so the decoder receives explicit values.
func buildOtpParameters(secretRaw []byte, name, issuer string, alg pbAlgorithm, digits pbDigits, otpType pbOtpType) []byte {
	var b []byte
	b = appendLenDelim(b, 1, secretRaw) // field 1: secret
	b = appendString(b, 2, name)        // field 2: name
	if issuer != "" {
		b = appendString(b, 3, issuer) // field 3: issuer
	}
	b = appendEnum(b, 4, uint64(alg))     // field 4: algorithm (always write)
	b = appendEnum(b, 5, uint64(digits))  // field 5: digits   (always write)
	b = appendEnum(b, 6, uint64(otpType)) // field 6: type
	return b
}

// buildMigrationPayload encodes a complete MigrationPayload protobuf.
func buildMigrationPayload(entries [][]byte, version, batchSize, batchIndex, batchID int32) []byte {
	var b []byte
	for _, e := range entries {
		b = appendLenDelim(b, 1, e) // field 1: otp_parameters (repeated)
	}
	b = appendEnum(b, 2, uint64(version))
	b = appendEnum(b, 3, uint64(batchSize))
	b = appendEnum(b, 4, uint64(batchIndex))
	b = appendEnum(b, 5, uint64(batchID))
	return b
}

// buildMigrationURI encodes entries into an otpauth-migration:// URI.
// The base64 data is URL-percent-encoded so that '+' and '/' survive url.Parse.
func buildMigrationURI(entries [][]byte) string {
	payload := buildMigrationPayload(entries, 1, int32(len(entries)), 0, 12345)
	data := base64.StdEncoding.EncodeToString(payload)
	// Percent-encode characters that are special in query strings.
	data = strings.ReplaceAll(data, "+", "%2B")
	data = strings.ReplaceAll(data, "/", "%2F")
	data = strings.ReplaceAll(data, "=", "%3D")
	return "otpauth-migration://offline?data=" + data
}

// decodeBase32Secret decodes a base32 secret string into raw bytes.
func decodeBase32Secret(s string) []byte {
	s = strings.ToUpper(strings.TrimRight(s, "="))
	b, err := base32.StdEncoding.DecodeString(pad32(s))
	if err != nil {
		panic("test helper: bad base32: " + err.Error())
	}
	return b
}

// ─── Sample secrets ───────────────────────────────────────────────────────────

// These are the RFC 6238 Appendix B SHA1 test secret (base32: JBSWY3DPEHPK3PXP).
// Using well-known test values ensures deterministic code expectations.
const (
	testSecret1B32 = "JBSWY3DPEHPK3PXP"                 // "Hello!\xDE\xAD\xBE\xEF" base32
	testSecret2B32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" // "12345678901234567890" base32
)

// ─── Tests: proto hand-decoder ────────────────────────────────────────────────

func TestProtoVarintRoundtrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 255, 300, 16383, 16384, 1<<21 - 1, 1 << 21, 1<<28 - 1}
	for _, v := range cases {
		buf := appendVarint(nil, v)
		r := newProtoReader(buf)
		got, err := r.readVarint()
		if err != nil {
			t.Fatalf("readVarint(%d): %v", v, err)
		}
		if got != v {
			t.Errorf("varint roundtrip: want %d, got %d", v, got)
		}
		if !r.done() {
			t.Errorf("varint roundtrip: reader not exhausted for value %d", v)
		}
	}
}

func TestProtoDecodeSingleOtpParameters(t *testing.T) {
	secretRaw := decodeBase32Secret(testSecret1B32)
	entry := buildOtpParameters(secretRaw, "alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)

	params, err := decodeOtpParameters(entry)
	if err != nil {
		t.Fatalf("decodeOtpParameters: %v", err)
	}
	if params.Name != "alice@example.com" {
		t.Errorf("Name: want %q, got %q", "alice@example.com", params.Name)
	}
	if params.Issuer != "GitHub" {
		t.Errorf("Issuer: want %q, got %q", "GitHub", params.Issuer)
	}
	if params.Type != pbOtpTypeTOTP {
		t.Errorf("Type: want %v, got %v", pbOtpTypeTOTP, params.Type)
	}
	if string(params.Secret) != string(secretRaw) {
		t.Errorf("Secret mismatch")
	}
}

func TestProtoDecodeMigrationPayload(t *testing.T) {
	secret1Raw := decodeBase32Secret(testSecret1B32)
	secret2Raw := decodeBase32Secret(testSecret2B32)

	e1 := buildOtpParameters(secret1Raw, "GitHub:alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	e2 := buildOtpParameters(secret2Raw, "bob@example.com", "Gmail", pbAlgorithmSHA256, pbDigitsEight, pbOtpTypeTOTP)

	payload := buildMigrationPayload([][]byte{e1, e2}, 1, 2, 0, 99)
	mp, err := decodeMigrationPayload(payload)
	if err != nil {
		t.Fatalf("decodeMigrationPayload: %v", err)
	}
	if len(mp.OtpParameters) != 2 {
		t.Fatalf("OtpParameters: want 2, got %d", len(mp.OtpParameters))
	}
	if mp.Version != 1 {
		t.Errorf("Version: want 1, got %d", mp.Version)
	}
	if mp.BatchSize != 2 {
		t.Errorf("BatchSize: want 2, got %d", mp.BatchSize)
	}

	p0 := mp.OtpParameters[0]
	if p0.Issuer != "GitHub" {
		t.Errorf("p0 Issuer: want GitHub, got %q", p0.Issuer)
	}
	if p0.Algorithm != pbAlgorithmSHA1 {
		t.Errorf("p0 Algorithm: want SHA1, got %v", p0.Algorithm)
	}

	p1 := mp.OtpParameters[1]
	if p1.Issuer != "Gmail" {
		t.Errorf("p1 Issuer: want Gmail, got %q", p1.Issuer)
	}
	if p1.Algorithm != pbAlgorithmSHA256 {
		t.Errorf("p1 Algorithm: want SHA256, got %v", p1.Algorithm)
	}
	if p1.Digits != pbDigitsEight {
		t.Errorf("p1 Digits: want 8, got %v", p1.Digits)
	}
}

// ─── Tests: URI parser ────────────────────────────────────────────────────────

func TestParseMigrationURI(t *testing.T) {
	secret1Raw := decodeBase32Secret(testSecret1B32)
	secret2Raw := decodeBase32Secret(testSecret2B32)

	e1 := buildOtpParameters(secret1Raw, "GitHub:alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	e2 := buildOtpParameters(secret2Raw, "bob@bank.com", "MyBank", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	uri := buildMigrationURI([][]byte{e1, e2})

	mp, err := ParseMigrationURI(uri)
	if err != nil {
		t.Fatalf("ParseMigrationURI: %v", err)
	}
	if len(mp.OtpParameters) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(mp.OtpParameters))
	}
}

func TestParseMigrationURIRejectsBadScheme(t *testing.T) {
	_, err := ParseMigrationURI("otpauth://totp/foo?secret=JBSWY3DPEHPK3PXP")
	if err == nil {
		t.Fatal("expected error for non-migration URI")
	}
}

func TestParseMigrationURIRejectsMissingData(t *testing.T) {
	_, err := ParseMigrationURI("otpauth-migration://offline")
	if err == nil {
		t.Fatal("expected error for missing data param")
	}
}

// ─── Tests: sample payload imports all accounts ───────────────────────────────

func TestSampleMigrationImportsAll(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}

	secret1Raw := decodeBase32Secret(testSecret1B32)
	secret2Raw := decodeBase32Secret(testSecret2B32)

	e1 := buildOtpParameters(secret1Raw, "GitHub:alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	e2 := buildOtpParameters(secret2Raw, "bob@gmail.com", "Gmail", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	uri := buildMigrationURI([][]byte{e1, e2})

	mp, err := ParseMigrationURI(uri)
	if err != nil {
		t.Fatalf("ParseMigrationURI: %v", err)
	}

	accounts, errs := importIntoStore(store, mp.OtpParameters)
	if len(errs) != 0 {
		t.Errorf("unexpected import errors: %v", errs)
	}
	if len(accounts) != 2 {
		t.Fatalf("want 2 imported accounts, got %d", len(accounts))
	}

	// Verify accounts landed in the store.
	list := store.List()
	if len(list) != 2 {
		t.Errorf("store.List() returned %d accounts, want 2", len(list))
	}

	// Verify issuer / service split.
	foundGitHub := false
	foundGmail := false
	for _, a := range list {
		switch a.Issuer {
		case "GitHub":
			foundGitHub = true
			if a.AccountID != "alice@example.com" {
				t.Errorf("GitHub account ID: want alice@example.com, got %q", a.AccountID)
			}
		case "Gmail":
			foundGmail = true
		}
	}
	if !foundGitHub {
		t.Error("GitHub account not found in store")
	}
	if !foundGmail {
		t.Error("Gmail account not found in store")
	}
}

func TestHOTPSkipped(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}

	secretRaw := decodeBase32Secret(testSecret1B32)
	eHOTP := buildOtpParameters(secretRaw, "hotp_acct@example.com", "SomeService", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeHOTP)
	eTOTP := buildOtpParameters(secretRaw, "totp_acct@example.com", "OtherService", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)

	params := []otpParameters{}
	for _, raw := range [][]byte{eHOTP, eTOTP} {
		p, err := decodeOtpParameters(raw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		params = append(params, p)
	}

	accounts, errs := importIntoStore(store, params)
	if len(accounts) != 1 {
		t.Errorf("want 1 TOTP imported, got %d", len(accounts))
	}
	if len(errs) != 1 {
		t.Errorf("want 1 warning (HOTP skipped), got %d", len(errs))
	}
}

// ─── Tests: export → re-import identical ─────────────────────────────────────

func TestExportReimportIdentical(t *testing.T) {
	srcDir := t.TempDir()
	srcStore, err := NewStoreAt(srcDir)
	if err != nil {
		t.Fatalf("NewStoreAt src: %v", err)
	}

	// Populate with 3 accounts via migration URI.
	secret1Raw := decodeBase32Secret(testSecret1B32)
	secret2Raw := decodeBase32Secret(testSecret2B32)
	e1 := buildOtpParameters(secret1Raw, "GitHub:alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	e2 := buildOtpParameters(secret2Raw, "carol@bank.com", "FNB", pbAlgorithmSHA256, pbDigitsEight, pbOtpTypeTOTP)
	e3 := buildOtpParameters(secret1Raw, "dave@example.com", "Slack", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	uri := buildMigrationURI([][]byte{e1, e2, e3})

	mp, err := ParseMigrationURI(uri)
	if err != nil {
		t.Fatalf("ParseMigrationURI: %v", err)
	}
	_, errs := importIntoStore(srcStore, mp.OtpParameters)
	if len(errs) != 0 {
		t.Fatalf("import errors: %v", errs)
	}

	// Export.
	blob, err := buildExportBlob(srcStore)
	if err != nil {
		t.Fatalf("buildExportBlob: %v", err)
	}
	if blob.Count != 3 {
		t.Errorf("blob.Count: want 3, got %d", blob.Count)
	}

	// Re-import into a fresh store (same key — same srcDir, new Store instance).
	dstStore, err := NewStoreAt(srcDir)
	if err != nil {
		t.Fatalf("NewStoreAt dst: %v", err)
	}
	// Clear the dst store (it loaded the same keychain as src).
	// Use a fresh temp dir so we get an empty store with the same key.
	dstDir := t.TempDir()
	// Copy keyfile so decryption key matches.
	keyfileSrc, _ := os.ReadFile(srcDir + "/keyfile")
	os.WriteFile(dstDir+"/keyfile", keyfileSrc, 0600)
	dstStore, err = NewStoreAt(dstDir)
	if err != nil {
		t.Fatalf("NewStoreAt dst2: %v", err)
	}

	imported, err := importExportBlob(dstStore, blob)
	if err != nil {
		t.Fatalf("importExportBlob: %v", err)
	}
	if len(imported) != 3 {
		t.Fatalf("want 3 re-imported accounts, got %d", len(imported))
	}

	// Verify secrets survive the round-trip by generating codes from both stores.
	srcList := srcStore.List()
	dstList := dstStore.List()
	if len(srcList) != len(dstList) {
		t.Fatalf("list length mismatch: src=%d dst=%d", len(srcList), len(dstList))
	}

	// Build a map of service → (code, digits, period) from src.
	type codeKey struct {
		code   string
		digits int
		period int
	}
	srcCodes := map[string]codeKey{}
	for _, acc := range srcList {
		code, _, err := srcStore.Code(acc.ID)
		if err != nil {
			t.Fatalf("srcStore.Code(%s): %v", acc.ID, err)
		}
		srcCodes[acc.Issuer+"/"+acc.AccountID] = codeKey{code, acc.Digits, acc.Period}
	}

	for _, acc := range dstList {
		code, _, err := dstStore.Code(acc.ID)
		if err != nil {
			t.Fatalf("dstStore.Code(%s): %v", acc.ID, err)
		}
		key := acc.Issuer + "/" + acc.AccountID
		src, ok := srcCodes[key]
		if !ok {
			t.Errorf("dstList has account %q not found in srcCodes", key)
			continue
		}
		if code != src.code {
			t.Errorf("account %q: code mismatch src=%s dst=%s", key, src.code, code)
		}
		if acc.Digits != src.digits {
			t.Errorf("account %q: digits mismatch src=%d dst=%d", key, src.digits, acc.Digits)
		}
		if acc.Period != src.period {
			t.Errorf("account %q: period mismatch src=%d dst=%d", key, src.period, acc.Period)
		}
	}
}

// ─── Tests: base32Encode ──────────────────────────────────────────────────────

func TestBase32Encode(t *testing.T) {
	// Verify our hand-rolled base32Encode matches the stdlib for all lengths 0-20.
	for n := 0; n <= 20; n++ {
		input := make([]byte, n)
		for i := range input {
			input[i] = byte(i * 7)
		}
		want := strings.TrimRight(base32.StdEncoding.EncodeToString(input), "=")
		got := base32Encode(input)
		if got != want {
			t.Errorf("base32Encode(%d bytes): want %q, got %q", n, want, got)
		}
	}
}

// ─── Tests: export blob encryption ───────────────────────────────────────────

func TestExportBlobIsEncrypted(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}

	_, err = store.AddManual("TestService", "TestIssuer", "user@test.com", testSecret1B32)
	if err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	blob, err := buildExportBlob(store)
	if err != nil {
		t.Fatalf("buildExportBlob: %v", err)
	}

	// Ciphertext must not contain the plaintext secret.
	ct, _ := base64.StdEncoding.DecodeString(blob.Ciphertext)
	if strings.Contains(string(ct), testSecret1B32) {
		t.Error("ciphertext contains plaintext secret — encryption is not working")
	}
	if blob.Version != 1 {
		t.Errorf("Version: want 1, got %d", blob.Version)
	}
	if blob.Count != 1 {
		t.Errorf("Count: want 1, got %d", blob.Count)
	}
}
