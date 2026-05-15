// Package ulid provides a minimal Crockford-base32 ULID generator that
// makes no network calls and has no external dependencies.
//
// Format: 48-bit millisecond timestamp (10 chars) + 80-bit random (16 chars) = 26 chars.
// Alphabet: Crockford base32 – "0123456789ABCDEFGHJKMNPQRSTVWXYZ".
//
// NewULID is safe for concurrent use.
package ulid

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var mu sync.Mutex

// NewULID generates a fresh ULID string. It panics only if the OS random
// source is completely unavailable (extremely unlikely).
func NewULID() string {
	mu.Lock()
	defer mu.Unlock()

	now := uint64(time.Now().UnixMilli())

	// 10 chars for 48-bit timestamp
	ts := make([]byte, 10)
	for i := 9; i >= 0; i-- {
		ts[i] = alphabet[now&0x1F]
		now >>= 5
	}

	// 16 chars for 80-bit randomness (10 bytes → 16 base32 chars)
	var rndBytes [10]byte
	if _, err := rand.Read(rndBytes[:]); err != nil {
		panic(fmt.Sprintf("ulid: crypto/rand unavailable: %v", err))
	}
	// Encode 80 bits into 16 base32 characters (5 bits each)
	rnd := make([]byte, 16)
	// Pack 10 bytes into a 80-bit integer via two uint64s (upper 64 + lower 16)
	hi := binary.BigEndian.Uint64(rndBytes[0:8])
	lo := uint64(rndBytes[8])<<8 | uint64(rndBytes[9])
	// lo is only 16 bits; encode 16 chars total = 80 bits
	// encode from right: first 3 chars from lo (15 bits), rest from hi
	for i := 15; i >= 13; i-- {
		rnd[i] = alphabet[lo&0x1F]
		lo >>= 5
	}
	// remaining 65 bits... use hi for chars 12..0
	for i := 12; i >= 0; i-- {
		rnd[i] = alphabet[hi&0x1F]
		hi >>= 5
	}

	return string(ts) + string(rnd)
}

// LoadOrCreate reads the persisted instance-id from dir/instance-id,
// generating and saving a new ULID if the file does not exist.
// dir is typically ~/.vulos.
func LoadOrCreate(dir string) (string, error) {
	path := filepath.Join(dir, "instance-id")

	if data, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(data))
		if len(id) == 26 {
			return id, nil
		}
	}

	id := NewULID()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return id, fmt.Errorf("ulid: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0600); err != nil {
		return id, fmt.Errorf("ulid: write %s: %w", path, err)
	}
	return id, nil
}
