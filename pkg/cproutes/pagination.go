// pagination.go — PAGINATE-01: shared bounded limit/offset pagination for the
// control-plane list endpoints.
//
// Before this, list endpoints either used a fixed cap (orgadmin members/usage)
// or accepted an unbounded client limit (fleet device lists). This helper gives
// every list a uniform, bounded contract:
//
//   - limit  — clamped to [1, maxPageSize]; default defaultPageSize.
//   - offset — clamped to >= 0 (derived from an opaque cursor when supplied).
//   - cursor — an opaque, stable token encoding the next offset; round-trips so
//     a client need not track offsets itself.
//
// Stable ordering is the caller's responsibility (the underlying SQL ORDER BY);
// this helper only standardises the windowing + cursor encoding. The cursor is
// base64url(offset) — opaque to clients, cheap to mint, and validated on input.
package cproutes

import (
	"encoding/base64"
	"net/http"
	"strconv"
)

const (
	// defaultPageSize is the page size when the client omits limit.
	defaultPageSize = 50
	// maxPageSize is the hard upper bound on a single page, regardless of what
	// the client asks for — prevents an unbounded scan / large response.
	maxPageSize = 200
)

// pageParams is the resolved, bounded window for a list request.
type pageParams struct {
	Limit  int // in [1, maxPageSize]
	Offset int // >= 0
}

// parsePageParams resolves limit/offset/cursor query params into a bounded
// window. Precedence for the offset: an explicit ?cursor= (opaque) wins;
// otherwise ?offset=. The limit is always clamped to [1, maxPageSize].
func parsePageParams(r *http.Request) pageParams {
	q := r.URL.Query()

	limit := defaultPageSize
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if limit < 1 {
		limit = 1
	}

	offset := 0
	if cur := q.Get("cursor"); cur != "" {
		if dec, ok := decodeCursor(cur); ok {
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
	return pageParams{Limit: limit, Offset: offset}
}

// nextCursor returns the opaque cursor for the NEXT page, or "" when the page
// just returned was the last one. returnedCount is how many items the current
// page actually returned: when it is < the requested limit there is no further
// page (so no cursor), avoiding an extra empty round-trip.
func (p pageParams) nextCursor(returnedCount int) string {
	if returnedCount < p.Limit {
		return "" // last page — no further cursor.
	}
	return encodeCursor(p.Offset + p.Limit)
}

// encodeCursor mints an opaque base64url cursor from an offset.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodeCursor parses an opaque cursor back to an offset. ok=false on garbage
// (the caller then falls back to offset 0).
func decodeCursor(cur string) (int, bool) {
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

// pagedResponse is the uniform list envelope: the items under "items" plus
// pagination metadata. next_cursor is "" on the last page.
type pagedResponse struct {
	Items      any    `json:"items"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	NextCursor string `json:"next_cursor,omitempty"`
	Count      int    `json:"count"`
}

// newPagedResponse builds a pagedResponse for a page of items. count is the
// number of items in this page (used to decide whether a next cursor exists).
func newPagedResponse(p pageParams, items any, count int) pagedResponse {
	return pagedResponse{
		Items:      items,
		Limit:      p.Limit,
		Offset:     p.Offset,
		NextCursor: p.nextCursor(count),
		Count:      count,
	}
}
