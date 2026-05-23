package auth

// recovery_store.go — implements internal/auth.RecoveryStore on the auth.Store.
//
// VUMAIL-06: encrypted recovery-kit blobs are persisted in the `recovery_blobs`
// SQLite table (one row per user_id).  The blob is the opaque, client-encrypted
// value — the auth store never holds plaintext.
//
// Degraded mode: when db == nil (in-memory-only store) the methods fall back to
// an in-memory map so the server still works in dev / no-disk environments.

import (
	"fmt"
	"log"
	"sync"
)

// recoveryBlobMu guards the in-memory fallback map used when db == nil.
var recoveryBlobMu sync.RWMutex
var recoveryBlobMem = map[string][]byte{} // user_id → blob

// SaveRecoveryBlob persists the encrypted recovery blob for userID.
// Implements internal/auth.RecoveryStore.
func (s *Store) SaveRecoveryBlob(userID string, blob []byte) error {
	if userID == "" {
		return fmt.Errorf("auth: SaveRecoveryBlob: userID must not be empty")
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)

	if s.db != nil {
		_, err := s.db.Exec(
			`INSERT INTO recovery_blobs(user_id, blob) VALUES(?, ?)
			 ON CONFLICT(user_id) DO UPDATE SET blob=excluded.blob`,
			userID, cp,
		)
		if err != nil {
			log.Printf("[auth] sqlite: save recovery blob for %s: %v", userID, err)
			return fmt.Errorf("auth: SaveRecoveryBlob: %w", err)
		}
		return nil
	}

	// Degraded / in-memory mode.
	recoveryBlobMu.Lock()
	defer recoveryBlobMu.Unlock()
	recoveryBlobMem[userID] = cp
	return nil
}

// LoadRecoveryBlob returns the encrypted recovery blob for userID, or
// (nil, nil) if none has been stored.
// Implements internal/auth.RecoveryStore.
func (s *Store) LoadRecoveryBlob(userID string) ([]byte, error) {
	if userID == "" {
		return nil, nil
	}

	if s.db != nil {
		var blob []byte
		err := s.db.QueryRow(
			`SELECT blob FROM recovery_blobs WHERE user_id=?`, userID,
		).Scan(&blob)
		if err != nil {
			// sql.ErrNoRows → not found, return nil, nil per interface contract.
			return nil, nil
		}
		cp := make([]byte, len(blob))
		copy(cp, blob)
		return cp, nil
	}

	// Degraded / in-memory mode.
	recoveryBlobMu.RLock()
	defer recoveryBlobMu.RUnlock()
	b := recoveryBlobMem[userID]
	if b == nil {
		return nil, nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}
