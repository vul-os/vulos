package cproutes

import "crypto/sha256"

// boxULID derives a stable per-account bucket ULID from an accountID: a
// deterministic 26-char Crockford base32 string derived from the SHA-256 of the
// account ID, so each account always maps to one canonical storage identity.
//
// It is retained (from the removed box-provisioning path) because the account's
// unified storage/backup bucket name is derived from it — see routes_storage.go /
// files.go. It no longer implies any hosted box: Vulos provisions no
// compute; this is purely the account's storage-bucket key.
func boxULID(accountID string) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	h := sha256.Sum256([]byte("box:" + accountID))
	out := make([]byte, 26)
	for i := range out {
		out[i] = alphabet[h[i%32]&0x1F]
	}
	return string(out)
}
