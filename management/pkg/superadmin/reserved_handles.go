package superadmin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ReservedHandle is one entry in the merged reserved-handles list.
type ReservedHandle struct {
	Handle   string `json:"handle"`
	Reason   string `json:"reason"`
	ReadOnly bool   `json:"read_only"` // true = from hard-coded map
	AddedBy  string `json:"added_by,omitempty"`
	AddedAt  string `json:"added_at,omitempty"`
}

// ListReservedHandles returns both hard-coded and DB-backed reserved handles.
// Hard-coded entries are marked ReadOnly=true.
func (s *Store) ListReservedHandles() ([]ReservedHandle, error) {
	// Hard-coded floor.
	hardcoded := hardcodedReservedHandles()
	out := make([]ReservedHandle, 0, len(hardcoded)+16)
	for h := range hardcoded {
		out = append(out, ReservedHandle{Handle: h, Reason: "system", ReadOnly: true})
	}

	// DB-backed additions.
	rows, err := s.db.Query(
		`SELECT handle, reason, COALESCE(added_by, ''), added_at
		 FROM additional_reserved_handles ORDER BY added_at DESC`,
	)
	if err != nil {
		return out, nil // table may not yet exist in all envs
	}
	defer rows.Close()
	for rows.Next() {
		var r ReservedHandle
		if err := rows.Scan(&r.Handle, &r.Reason, &r.AddedBy, &r.AddedAt); err == nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// AddReservedHandle adds handle to the DB-backed table.
func (s *Store) AddReservedHandle(handle, reason, addedByAccountID string, al *auditlog.Logger) error {
	ctx := context.Background()
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO additional_reserved_handles (handle, reason, added_by, added_at) VALUES (?, ?, ?, ?)`),
		handle, reason, nullableStr(addedByAccountID), time.Now().UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return fmt.Errorf("superadmin: handle %q already reserved", handle)
		}
		return fmt.Errorf("superadmin: add reserved handle: %w", err)
	}
	// Get actor email.
	var actorEmail string
	_ = s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT email FROM users WHERE id = ?`), addedByAccountID).Scan(&actorEmail)
	auditAction(ctx, al, actorEmail, "admin.handle.reserve", handle,
		map[string]string{"reason": reason})
	return nil
}

// DeleteReservedHandle removes handle from the DB-backed table.
// Returns 409 error if handle is in the hard-coded map.
func (s *Store) DeleteReservedHandle(handle, actorAccountID string, al *auditlog.Logger) error {
	if hardcodedReservedHandles()[handle] {
		return errHardcoded
	}
	ctx := context.Background()
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM additional_reserved_handles WHERE handle = ?`), handle,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: delete reserved handle: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("superadmin: handle %q not found in DB list", handle)
	}
	var actorEmail string
	_ = s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT email FROM users WHERE id = ?`), actorAccountID).Scan(&actorEmail)
	auditAction(ctx, al, actorEmail, "admin.handle.unreserve", handle, nil)
	return nil
}

// IsReservedHandle returns true if handle is in either the hard-coded or DB list.
func (s *Store) IsReservedHandleDB(handle string) bool {
	if hardcodedReservedHandles()[handle] {
		return true
	}
	var count int
	_ = s.db.QueryRow(
		s.db.Rebind(`SELECT COUNT(*) FROM additional_reserved_handles WHERE handle = ?`), handle,
	).Scan(&count)
	return count > 0
}

var errHardcoded = errors.New("superadmin: cannot delete hard-coded reserved handle")

// hardcodedReservedHandles returns a copy of the auth package's reserved set
// (duplicated here to avoid a cyclic import).
func hardcodedReservedHandles() map[string]bool {
	return map[string]bool{
		"admin": true, "administrator": true, "abuse": true, "help": true,
		"support": true, "security": true, "postmaster": true, "root": true,
		"noreply": true, "no-reply": true, "mailer-daemon": true, "www": true,
		"mail": true, "system": true, "vulos": true, "info": true,
		"contact": true, "sales": true, "billing": true, "legal": true,
		"privacy": true, "hello": true, "hi": true, "hey": true,
		"owner": true, "ceo": true, "founder": true,
	}
}

func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP handlers — reserved handles
// ─────────────────────────────────────────────────────────────────────────────

// HandleListReservedHandles handles GET /api/superadmin/reserved-handles
func HandleListReservedHandles(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := store.ListReservedHandles()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"handles": list})
	}
}

// HandleAddReservedHandle handles POST /api/superadmin/reserved-handles
func HandleAddReservedHandle(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Handle string `json:"handle"`
			Reason string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Handle == "" {
			jsonError(w, "handle and reason required", http.StatusBadRequest)
			return
		}
		actor := AdminAccountIDFromCtx(r.Context())
		if err := store.AddReservedHandle(body.Handle, body.Reason, actor, al); err != nil {
			jsonError(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

// HandleDeleteReservedHandle handles DELETE /api/superadmin/reserved-handles/{handle}
func HandleDeleteReservedHandle(store *Store, al *auditlog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handle := r.PathValue("handle")
		actor := AdminAccountIDFromCtx(r.Context())
		if err := store.DeleteReservedHandle(handle, actor, al); err != nil {
			if errors.Is(err, errHardcoded) {
				jsonError(w, "cannot delete hard-coded reserved handle", http.StatusConflict)
				return
			}
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
