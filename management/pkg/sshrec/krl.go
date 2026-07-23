package sshrec

import (
	"context"
	"encoding/binary"
)

// BuildKRL constructs a minimal OpenSSH KRL binary that lists all revoked cert
// key-ids reported by the store.
//
// Format per OpenSSH krl.c / PROTOCOL.krl:
//
//	[magic: 8 bytes] [format_version: 4] [krl_version: 8] [date_seconds: 8]
//	[flags: 8] [reserved: 8-byte len-prefixed] [comment: 8-byte len-prefixed]
//	[section …]
//
// Each KRL_SECTION_CERT_KEY_ID section (type 2) contains:
//
//	[section_type: 1] [section_body_len: 4] [key_id_string: 4-len prefixed]…
//
// We use the simple "explicit key-id" approach (one section per revoked id).
// v1 keeps this straightforward; a future wave can switch to serial-ranges.
func BuildKRL(ctx context.Context, st Store) ([]byte, error) {
	ids, err := st.ListRevoked(ctx)
	if err != nil {
		return nil, err
	}
	return buildKRLFromIDs(ids), nil
}

// krlMagic is the 8-byte magic identifying an OpenSSH KRL file.
var krlMagic = []byte("SSHKRL\n\x00")

const (
	krlFormatVersion = 1
	krlSectCertKeyID = 2 // KRL_SECTION_CERT_KEY_ID
)

func buildKRLFromIDs(ids []string) []byte {
	// --- KRL header ---
	// magic(8) + format_version(4) + krl_version(8) + date(8)
	// + flags(8) + reserved(4+0) + comment(4+0)
	hdr := make([]byte, 0, 64)
	hdr = append(hdr, krlMagic...)
	hdr = appendUint32(hdr, krlFormatVersion)
	hdr = appendUint64(hdr, 0)  // krl_version
	hdr = appendUint64(hdr, 0)  // date
	hdr = appendUint64(hdr, 0)  // flags
	hdr = appendString(hdr, "") // reserved
	hdr = appendString(hdr, "") // comment

	if len(ids) == 0 {
		return hdr
	}

	// --- one KRL_SECTION_CERT_KEY_ID section containing all key-ids ---
	body := make([]byte, 0, len(ids)*40)
	for _, id := range ids {
		body = appendString(body, id)
	}

	sec := make([]byte, 0, 1+4+len(body))
	sec = append(sec, krlSectCertKeyID)
	sec = appendUint32(sec, uint32(len(body)))
	sec = append(sec, body...)

	return append(hdr, sec...)
}

// appendUint32 appends a big-endian uint32.
func appendUint32(b []byte, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return append(b, buf[:]...)
}

// appendUint64 appends a big-endian uint64.
func appendUint64(b []byte, v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return append(b, buf[:]...)
}

// appendString appends a length-prefixed (uint32 BE) string (SSH wire format).
func appendString(b []byte, s string) []byte {
	b = appendUint32(b, uint32(len(s)))
	return append(b, s...)
}
