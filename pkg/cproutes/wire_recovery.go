package cproutes

// wire_recovery.go — opens the account-recovery store (internal/auth/recovery)
// and resolves RECOVERY_KEK.
//
// The engine has always existed and has never been opened by the server: only
// cmd/migrate touched it, so /api/auth/recovery/submit — the one call the
// Account Recovery page makes — resolved to nothing. This is where it comes
// alive.
//
// RECOVERY_KEK is the AES-256 key wrapping the per-upload DEKs of the
// government-ID documents a reviewer needs. It FAILS CLOSED:
//
//   - absent      → the store opens with a nil KEK. Submit / cancel / review /
//     complete all work; the ID-upload route answers 503. recovery.StoreIDUpload
//     refuses a nil KEK outright, so a plaintext ID can never be written.
//   - malformed   → same, and in prod the process REFUSES TO START rather than
//     run with an ID-upload path that silently cannot work.
//
// The KEK is read through the secrets seam (secretOrEnv), like every other
// credential: a secrets-manager value wins, then the environment.

import (
	"context"
	"encoding/base64"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auth/recovery"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
)

// recoveryPurgeSvc is the recovery store the scheduler's ID-upload purge job
// sweeps. Set once by wireRecovery; nil (recovery failed to open) makes the job
// a no-op. Same package-level-handoff idiom as filesPurgeSvc (wire_files.go).
var recoveryPurgeSvc *recovery.Store

// recoveryIDDir is the directory encrypted government-ID uploads are written to.
// Empty means uploads are refused — either the KEK is absent/malformed or the
// directory could not be created.
var recoveryIDDir string

// wireRecovery opens the recovery DB through the cpdb seam and returns a ready
// Store, or nil when the DB cannot be opened (every /api/auth/recovery/* route
// then answers 503 — an honest outage, since a recovery request that is not
// durably recorded is worse than none).
func wireRecovery(ctx context.Context, dbDir string) *recovery.Store {
	kek := loadRecoveryKEK(ctx)

	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[recovery] WARNING: could not set DB dir (%v) — /api/auth/recovery/* routes return 503", err)
		return nil
	}
	db, err := cpdb.Open("recovery")
	if err != nil {
		log.Printf("[recovery] WARNING: failed to open recovery DB (%v) — /api/auth/recovery/* routes return 503", err)
		return nil
	}
	st, err := recovery.Open(db, kek)
	if err != nil {
		_ = db.Close()
		log.Printf("[recovery] WARNING: failed to migrate recovery DB (%v) — /api/auth/recovery/* routes return 503", err)
		return nil
	}
	recoveryPurgeSvc = st

	if len(kek) == 32 {
		dir := os.Getenv("RECOVERY_ID_DIR")
		if dir == "" {
			dir = filepath.Join(dbDir, "recovery-id")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("[recovery] WARNING: could not create ID-upload dir %s (%v) — ID upload refused", dir, err)
		} else {
			recoveryIDDir = dir
		}
	}

	log.Printf("[recovery] account-recovery store opened (backend=%s, window=%s, id_upload=%s)",
		db.Backend(), recovery.RecoveryWindow, idUploadState())
	return st
}

func idUploadState() string {
	if recoveryIDDir == "" {
		return "REFUSED (no RECOVERY_KEK)"
	}
	return "enabled at " + recoveryIDDir
}

// loadRecoveryKEK resolves RECOVERY_KEK to exactly 32 bytes (base64-encoded, as
// AUTH_TOTP_KEK is), or nil. nil disables ID upload — it never downgrades to
// storing a plaintext identity document.
func loadRecoveryKEK(ctx context.Context) []byte {
	raw := strings.TrimSpace(secretOrEnv(ctx, "RECOVERY_KEK"))
	if raw == "" {
		log.Println("[recovery] RECOVERY_KEK is not set — government-ID upload is REFUSED (an ID is never stored in plaintext). Submit/cancel/review/complete are unaffected.")
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		if env.IsProdResolved() {
			log.Fatalf("[recovery] RECOVERY_KEK is set but is not valid base64 (%v) — refusing to start. Set it to a base64-encoded 32-byte key or unset it.", err)
		}
		log.Printf("[recovery] WARNING: RECOVERY_KEK is not valid base64 (%v) — government-ID upload is REFUSED", err)
		return nil
	}
	if len(key) != 32 {
		if env.IsProdResolved() {
			log.Fatalf("[recovery] RECOVERY_KEK must decode to exactly 32 bytes, got %d — refusing to start.", len(key))
		}
		log.Printf("[recovery] WARNING: RECOVERY_KEK must decode to exactly 32 bytes, got %d — government-ID upload is REFUSED", len(key))
		return nil
	}
	return key
}
