package orgadmin

// directory.go — FILES PHASE 3 (cloud side): the org/tenant member DIRECTORY +
// share-target RESOLUTION for the Drive "Share with…" picker.
//
// Why this lives here. The OS Files control-plane (Phase 1, the `vulos` repo)
// already stores cross-user shares as an ACL keyed by USER-ID and mints
// ACL-gated, object-scoped storage grants for an authorized recipient. What it
// CANNOT do is turn "alice@org" into the user-id its ACL needs, because the OS
// box is identity-blind to the rest of the cloud — the canonical org roster +
// the email↔account mapping live in the control-plane (fleet + auth). This file
// is the cloud IDENTITY layer the OS sharing ACL plugs into:
//
//   1. Directory — GET /api/org/directory: the people in the caller's org the
//      user may share with (id + display name + email), for the picker.
//   2. Resolution — GET /api/org/directory/resolve?email=…: turn an email/handle
//      into the user-id the Files ACL stores, so "share with alice@org" works.
//
// Both reuse the SAME MemberStore the org-admin dashboard uses (fleet roster
// joined to auth emails), so the directory is automatically:
//   - TENANT-SCOPED — the roster comes from the SESSION's tenant; a caller can
//     only ever see / resolve members of their own org. Cross-org lookups return
//     an empty directory / ErrNotFound (no existence disclosure), exactly like
//     every other org-admin route.
//   - CONSISTENT — the same source of truth as the Members tab, so a person who
//     can be invited/managed is exactly a person who can be shared with.
//
// Unlike the Members tab these reads are NOT admin-gated: ANY authenticated org
// member may enumerate the directory + resolve a share target, because sharing a
// file is a member-level action, not an admin one (mirrors handleListMembers,
// which is session-gated but not Authorize-gated).
//
// See FILES_SHARING_DIRECTORY.md for the end-to-end cross-user read model and
// the scoped cross-BOX follow-up.

import (
	"context"
	"sort"
	"strings"
)

// DirectoryEntry is one shareable principal as the Drive picker renders it: the
// user-id the Files ACL stores, plus a human label. Field names mirror the
// picker reads: e.id, e.name, e.email. It is deliberately a SUBSET of Member —
// no role / last_active — because the picker has no business surfacing org-admin
// metadata.
type DirectoryEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// DirectoryResponse is the GET /api/org/directory body. Members is always a
// non-nil slice so the picker renders an empty state rather than erroring.
type DirectoryResponse struct {
	Members []DirectoryEntry `json:"members"`
	Count   int              `json:"count"`
}

// Directory returns the principals in tenantID the caller may share with, for
// the Drive picker. callerID (when known) is excluded — you do not share with
// yourself; the Files ACL rejects sharing a node with its owner anyway. q is an
// optional case-insensitive substring filter over name + email. The result is
// sorted by display label for a stable picker order.
//
// A nil MemberStore (or a tenant with no roster) yields an empty directory, not
// an error — the picker degrades to "no one to share with".
func (s *Service) Directory(ctx context.Context, tenantID, callerID, q string) (DirectoryResponse, error) {
	empty := DirectoryResponse{Members: []DirectoryEntry{}}
	if s.Members == nil {
		return empty, nil
	}
	members, err := s.Members.ListMembers(ctx, tenantID)
	if err != nil {
		return DirectoryResponse{}, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	out := make([]DirectoryEntry, 0, len(members))
	for _, m := range members {
		if m.ID == "" {
			continue // a member with no resolvable id cannot be a share target
		}
		if callerID != "" && m.ID == callerID {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.Email
		}
		if q != "" && !strings.Contains(strings.ToLower(name), q) &&
			!strings.Contains(strings.ToLower(m.Email), q) {
			continue
		}
		out = append(out, DirectoryEntry{ID: m.ID, Name: name, Email: m.Email})
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li == lj {
			return out[i].ID < out[j].ID
		}
		return li < lj
	})
	return DirectoryResponse{Members: out, Count: len(out)}, nil
}

// ResolveShareTarget turns query (an email or a bare handle = the email
// local-part) into the DirectoryEntry whose ID is the user-id the Files ACL
// stores. Resolution is STRICTLY scoped to tenantID's roster, so a caller can
// only ever resolve a member of their OWN org; anything else is ErrNotFound (no
// cross-org existence disclosure).
//
// Matching, in order, against the org roster:
//  1. exact email (case-insensitive) — the unambiguous path the picker uses;
//  2. exact handle = email local-part (case-insensitive), accepted ONLY when it
//     identifies exactly one member (an ambiguous handle is ErrNotFound rather
//     than guessing).
//
// An empty query, a nil MemberStore, or no match all return ErrNotFound so the
// route surfaces a uniform 404.
func (s *Service) ResolveShareTarget(ctx context.Context, tenantID, query string) (DirectoryEntry, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || s.Members == nil {
		return DirectoryEntry{}, ErrNotFound
	}
	members, err := s.Members.ListMembers(ctx, tenantID)
	if err != nil {
		return DirectoryEntry{}, err
	}
	// Pass 1: exact email.
	for _, m := range members {
		if m.ID == "" || m.Email == "" {
			continue
		}
		if strings.ToLower(m.Email) == q {
			return entryFor(m), nil
		}
	}
	// Pass 2: bare handle (local-part), only if unambiguous. A query that already
	// contains '@' was an email and is handled above — don't re-interpret it.
	if !strings.Contains(q, "@") {
		var match *Member
		ambiguous := false
		for i := range members {
			m := members[i]
			if m.ID == "" || m.Email == "" {
				continue
			}
			local := strings.ToLower(emailLocalPart(m.Email))
			if local == q {
				if match != nil {
					ambiguous = true
					break
				}
				match = &members[i]
			}
		}
		if match != nil && !ambiguous {
			return entryFor(*match), nil
		}
	}
	return DirectoryEntry{}, ErrNotFound
}

// entryFor projects a Member onto a DirectoryEntry, falling back to email for a
// missing display name (matching Directory).
func entryFor(m Member) DirectoryEntry {
	name := m.Name
	if name == "" {
		name = m.Email
	}
	return DirectoryEntry{ID: m.ID, Name: name, Email: m.Email}
}

// emailLocalPart returns the part of an email before '@' (the whole string when
// there is no '@').
func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i >= 0 {
		return email[:i]
	}
	return email
}
