package orgadmin

// pagination.go — PAGINATE-01: bounded limit/offset + opaque cursor for the
// org-admin member roster. Kept standalone (no cmd/server import) so the
// package stays decoupled; the encoding matches cmd/server/pagination.go
// (base64url(offset)) so cursors are interchangeable across the API.

import (
	"encoding/base64"
	"net/http"
	"strconv"
)

const (
	defaultMemberPageSize = 50
	maxMemberPageSize     = 200
)

// memberPage is the resolved, bounded window for the members list.
type memberPage struct {
	limit  int // in [1, maxMemberPageSize]
	offset int // >= 0
}

// parseMemberPage resolves limit/offset/cursor query params into a bounded
// window. ?cursor= (opaque) takes precedence over ?offset=. The limit is
// clamped to [1, maxMemberPageSize] with a default of defaultMemberPageSize.
func parseMemberPage(r *http.Request) memberPage {
	q := r.URL.Query()

	limit := defaultMemberPageSize
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxMemberPageSize {
		limit = maxMemberPageSize
	}
	if limit < 1 {
		limit = 1
	}

	offset := 0
	if cur := q.Get("cursor"); cur != "" {
		if dec, ok := decodeMemberCursor(cur); ok {
			offset = dec
		}
	} else if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if offset < 0 {
		offset = 0
	}
	return memberPage{limit: limit, offset: offset}
}

// next returns the opaque cursor for the NEXT page, or "" when this was the
// last page (returnedCount < limit).
func (p memberPage) next(returnedCount int) string {
	if returnedCount < p.limit {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(p.offset + p.limit)))
}

func decodeMemberCursor(cur string) (int, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
