// cellcreds.go — persistence for per-cell scoped credentials (storage_cell_creds,
// STORAGE-ISOLATION.md §1.4).
//
// CellCredStore is a NARROW seam kept OUT of the frozen Store interface
// (contract.go) — it is satisfied by both *SQLStore and *MemStore so the rotation
// job and provisioning wiring can persist/rotate/revoke cell creds without
// touching the frozen contract. The stored secret is AES-GCM-encrypted under
// STORAGE_KEK, mirroring BYO secret handling; CellCred.Secret is never serialised
// to JSON.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// CellCred is one persisted per-cell scoped credential.
type CellCred struct {
	AccountID string    `json:"account_id"`
	ULID      string    `json:"ulid"`
	Bucket    string    `json:"bucket"`
	CredID    string    `json:"cred_id"`   // provider access-key id (for Revoke)
	PolicyID  string    `json:"policy_id"` // provider policy id (for revoke cleanup)
	AccessKey string    `json:"access_key"`
	Secret    string    `json:"-"` // decrypted; NEVER in JSON
	Endpoint  string    `json:"endpoint"`
	Region    string    `json:"region"`
	ExpiresAt time.Time `json:"expires_at,omitempty"` // zero = non-expiring
	CreatedAt time.Time `json:"created_at"`
	RotatedAt time.Time `json:"rotated_at,omitempty"`
}

// CellCredStore persists per-cell scoped credentials. Satisfied by *SQLStore and
// *MemStore. Kept separate from the frozen Store interface.
// The row PK is (account_id, ulid), so Get/Delete are ALWAYS scoped by BOTH:
// filtering on ulid alone could, if two rows ever shared a ulid, read or delete
// the WRONG tenant's credential (defence-in-depth against a ulid collision).
type CellCredStore interface {
	PutCellCred(ctx context.Context, c CellCred) error
	GetCellCred(ctx context.Context, accountID, ulid string) (CellCred, error)
	DeleteCellCred(ctx context.Context, accountID, ulid string) error
	ListCellCreds(ctx context.Context) ([]CellCred, error)
}

// ---------------------------------------------------------------------------
// SQLStore implementation
// ---------------------------------------------------------------------------

func (s *SQLStore) PutCellCred(ctx context.Context, c CellCred) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var secretEnc string
	if len(s.kek) == 32 {
		enc, err := aesGCMEncrypt(s.kek, c.Secret)
		if err != nil {
			return fmt.Errorf("storage: encrypt cell cred secret: %w", err)
		}
		secretEnc = enc
	} else {
		secretEnc = c.Secret // dev mode: no KEK (plaintext)
	}

	var expires sql.NullString
	if !c.ExpiresAt.IsZero() {
		expires = sql.NullString{String: c.ExpiresAt.UTC().Format(time.RFC3339), Valid: true}
	}
	var rotated sql.NullString
	if !c.RotatedAt.IsZero() {
		rotated = sql.NullString{String: c.RotatedAt.UTC().Format(time.RFC3339), Valid: true}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO storage_cell_creds
		   (account_id, ulid, bucket, cred_id, policy_id, access_key, secret_enc, endpoint, region, expires_at, created_at, rotated_at)
		   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, ulid) DO UPDATE SET
		   bucket     = excluded.bucket,
		   cred_id    = excluded.cred_id,
		   policy_id  = excluded.policy_id,
		   access_key = excluded.access_key,
		   secret_enc = excluded.secret_enc,
		   endpoint   = excluded.endpoint,
		   region     = excluded.region,
		   expires_at = excluded.expires_at,
		   rotated_at = excluded.rotated_at`),
		c.AccountID, c.ULID, c.Bucket, c.CredID,
		nullableStr(c.PolicyID), c.AccessKey, secretEnc,
		nullableStr(c.Endpoint), nullableStr(c.Region),
		expires, now, rotated,
	)
	if err != nil {
		return fmt.Errorf("storage: put cell cred: %w", err)
	}
	return nil
}

func (s *SQLStore) GetCellCred(ctx context.Context, accountID, ulid string) (CellCred, error) {
	var (
		c         CellCred
		policyID  sql.NullString
		secretEnc string
		endpoint  sql.NullString
		region    sql.NullString
		expires   sql.NullString
		createdAt string
		rotated   sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, ulid, bucket, cred_id, policy_id, access_key, secret_enc,
		        endpoint, region, expires_at, created_at, rotated_at
		   FROM storage_cell_creds WHERE account_id = ? AND ulid = ?`), accountID, ulid,
	).Scan(&c.AccountID, &c.ULID, &c.Bucket, &c.CredID, &policyID, &c.AccessKey, &secretEnc,
		&endpoint, &region, &expires, &createdAt, &rotated)
	if errors.Is(err, sql.ErrNoRows) {
		return CellCred{}, ErrUnknownAccount
	}
	if err != nil {
		return CellCred{}, fmt.Errorf("storage: get cell cred: %w", err)
	}
	c.PolicyID = policyID.String
	c.Endpoint = endpoint.String
	c.Region = region.String
	if secretEnc != "" && len(s.kek) == 32 {
		dec, derr := aesGCMDecrypt(s.kek, secretEnc)
		if derr != nil {
			return CellCred{}, fmt.Errorf("storage: decrypt cell cred secret: %w", derr)
		}
		c.Secret = dec
	} else {
		c.Secret = secretEnc // dev mode
	}
	if expires.Valid {
		c.ExpiresAt, _ = time.Parse(time.RFC3339, expires.String)
	}
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if rotated.Valid {
		c.RotatedAt, _ = time.Parse(time.RFC3339, rotated.String)
	}
	return c, nil
}

func (s *SQLStore) DeleteCellCred(ctx context.Context, accountID, ulid string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM storage_cell_creds WHERE account_id = ? AND ulid = ?`), accountID, ulid)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("storage: delete cell cred: %w", err)
	}
	return nil
}

func (s *SQLStore) ListCellCreds(ctx context.Context) ([]CellCred, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT account_id, ulid, bucket, cred_id, policy_id, access_key, secret_enc,
		        endpoint, region, expires_at, created_at, rotated_at
		   FROM storage_cell_creds`))
	if err != nil {
		return nil, fmt.Errorf("storage: list cell creds: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []CellCred
	for rows.Next() {
		var (
			c         CellCred
			policyID  sql.NullString
			secretEnc string
			endpoint  sql.NullString
			region    sql.NullString
			expires   sql.NullString
			createdAt string
			rotated   sql.NullString
		)
		if err := rows.Scan(&c.AccountID, &c.ULID, &c.Bucket, &c.CredID, &policyID, &c.AccessKey,
			&secretEnc, &endpoint, &region, &expires, &createdAt, &rotated); err != nil {
			return nil, fmt.Errorf("storage: scan cell cred: %w", err)
		}
		c.PolicyID = policyID.String
		c.Endpoint = endpoint.String
		c.Region = region.String
		if secretEnc != "" && len(s.kek) == 32 {
			if dec, derr := aesGCMDecrypt(s.kek, secretEnc); derr == nil {
				c.Secret = dec
			}
		} else {
			c.Secret = secretEnc
		}
		if expires.Valid {
			c.ExpiresAt, _ = time.Parse(time.RFC3339, expires.String)
		}
		c.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if rotated.Valid {
			c.RotatedAt, _ = time.Parse(time.RFC3339, rotated.String)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// compile-time assertion.
var _ CellCredStore = (*SQLStore)(nil)

// ---------------------------------------------------------------------------
// MemStore implementation
// ---------------------------------------------------------------------------

// memCellCreds is embedded into MemStore via a separate mutex-guarded map so the
// reference store also satisfies CellCredStore. Declared here to keep all
// cell-cred code in one file.
type memCellCreds struct {
	mu    sync.Mutex
	creds map[string]CellCred // (account_id, ulid) -> cred
}

func newMemCellCreds() *memCellCreds { return &memCellCreds{creds: map[string]CellCred{}} }

// cellCredKey composes the (account_id, ulid) primary key so two tenants that
// happen to share a ulid never collide in the reference store (mirrors the SQL
// PK). The NUL separator cannot appear in either component.
func cellCredKey(accountID, ulid string) string { return accountID + "\x00" + ulid }

func (m *MemStore) cellCreds() *memCellCreds {
	m.cellOnce.Do(func() { m.cell = newMemCellCreds() })
	return m.cell
}

func (m *MemStore) PutCellCred(_ context.Context, c CellCred) error {
	cc := m.cellCreds()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	cc.creds[cellCredKey(c.AccountID, c.ULID)] = c
	return nil
}

func (m *MemStore) GetCellCred(_ context.Context, accountID, ulid string) (CellCred, error) {
	cc := m.cellCreds()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	c, ok := cc.creds[cellCredKey(accountID, ulid)]
	if !ok {
		return CellCred{}, ErrUnknownAccount
	}
	return c, nil
}

func (m *MemStore) DeleteCellCred(_ context.Context, accountID, ulid string) error {
	cc := m.cellCreds()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	delete(cc.creds, cellCredKey(accountID, ulid))
	return nil
}

func (m *MemStore) ListCellCreds(_ context.Context) ([]CellCred, error) {
	cc := m.cellCreds()
	cc.mu.Lock()
	defer cc.mu.Unlock()
	out := make([]CellCred, 0, len(cc.creds))
	for _, c := range cc.creds {
		out = append(out, c)
	}
	return out, nil
}

// compile-time assertion.
var _ CellCredStore = (*MemStore)(nil)
