package contacts

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// merge.go — the pure merge/dedupe layer. Given the RawContacts from every
// source, it unions the ones that refer to the same person and folds each group
// into a single UnifiedContact whose fields are the UNION across the group (so no
// data is dropped) and whose Sources records every source that contributed.
//
// Matching is by, in order of confidence:
//   - a shared phone MATCH KEY  (last-9-digits; national ≡ international form),
//   - a shared normalised EMAIL,
//   - an exact normalised FULL NAME (>= 2 tokens) — the weakest signal, used to
//     unify e.g. a CardDAV card that has only an email with a phone entry that has
//     only a number, for the same "Thabo Mokoena". Single-token labels ("Mom")
//     are deliberately NOT merged on the name alone (see isFullName).
//
// The union is transitive (union-find), so A↔B by phone and B↔C by email folds
// A, B and C into one contact.

// Merge de-duplicates raw contacts from all sources into unified entries, sorted
// by display name (then id) for a stable, deterministic result.
func Merge(raws []RawContact) []UnifiedContact {
	n := len(raws)
	uf := newUnionFind(n)

	// First index seen for each key type; unioning collapses duplicates.
	phoneIdx := map[string]int{}
	emailIdx := map[string]int{}
	nameIdx := map[string]int{}

	for i, rc := range raws {
		for _, p := range rc.Phones {
			if k := phoneMatchKey(p); k != "" {
				if j, ok := phoneIdx[k]; ok {
					uf.union(i, j)
				} else {
					phoneIdx[k] = i
				}
			}
		}
		for _, e := range rc.Emails {
			if k := normEmail(e); k != "" {
				if j, ok := emailIdx[k]; ok {
					uf.union(i, j)
				} else {
					emailIdx[k] = i
				}
			}
		}
		if isFullName(rc.Name) {
			k := nameKey(rc.Name)
			if j, ok := nameIdx[k]; ok {
				uf.union(i, j)
			} else {
				nameIdx[k] = i
			}
		}
	}

	// Gather each union-find group.
	groups := map[int][]RawContact{}
	for i, rc := range raws {
		root := uf.find(i)
		groups[root] = append(groups[root], rc)
	}

	out := make([]UnifiedContact, 0, len(groups))
	for _, g := range groups {
		out = append(out, foldGroup(g))
	}

	sort.Slice(out, func(i, j int) bool {
		ni, nj := normName(out[i].Name), normName(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// foldGroup collapses one merged group into a single UnifiedContact.
func foldGroup(g []RawContact) UnifiedContact {
	// Order the constituents by source priority (vulos < phone < box-sim) so
	// "first non-empty" field choices prefer the more authoritative source, and
	// results are deterministic regardless of raw input order.
	sorted := append([]RawContact(nil), g...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Source.priority() < sorted[j].Source.priority()
	})

	var uc UnifiedContact

	// Name: prefer the most complete name (most tokens); tie-break by source
	// priority (sorted order). This keeps "Thabo Mokoena" over a SIM's "Thabo".
	bestTokens := -1
	for _, rc := range sorted {
		if t := len(nameTokens(rc.Name)); t > bestTokens && strings.TrimSpace(rc.Name) != "" {
			bestTokens = t
			uc.Name = strings.TrimSpace(rc.Name)
		}
	}

	// Phones / Emails: union across the group, de-duped by match key / normalised
	// form, preserving first-seen (source-priority) display form.
	uc.Phones = dedupePhones(sorted)
	uc.Emails = dedupeEmails(sorted)

	// Org / Note: first non-empty in source-priority order.
	for _, rc := range sorted {
		if uc.Org == "" && strings.TrimSpace(rc.Org) != "" {
			uc.Org = strings.TrimSpace(rc.Org)
		}
		if uc.Note == "" && strings.TrimSpace(rc.Note) != "" {
			uc.Note = strings.TrimSpace(rc.Note)
		}
	}

	// Sources: sorted, de-duplicated set.
	seenSrc := map[Source]bool{}
	var srcs []Source
	for _, rc := range sorted {
		if rc.Source != "" && !seenSrc[rc.Source] {
			seenSrc[rc.Source] = true
			srcs = append(srcs, rc.Source)
		}
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].priority() < srcs[j].priority() })
	uc.Sources = make([]string, len(srcs))
	for i, s := range srcs {
		uc.Sources[i] = string(s)
	}

	uc.ID = contactID(uc)
	return uc
}

// dedupePhones returns the union of display phone forms across the group, keeping
// the first (highest-priority-source) display form for each distinct match key.
func dedupePhones(sorted []RawContact) []string {
	seen := map[string]bool{}
	var out []string
	for _, rc := range sorted {
		for _, p := range rc.Phones {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			k := phoneMatchKey(p)
			if k == "" {
				k = p
			}
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// dedupeEmails returns the union of emails across the group, de-duped on the
// normalised form, keeping the first display form.
func dedupeEmails(sorted []RawContact) []string {
	seen := map[string]bool{}
	var out []string
	for _, rc := range sorted {
		for _, e := range rc.Emails {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			k := normEmail(e)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// contactID derives a stable id from the entry's match keys (phone keys + emails,
// or the name when neither is present). Order-independent, so the same person
// yields the same id regardless of source ordering.
func contactID(uc UnifiedContact) string {
	var keys []string
	for _, p := range uc.Phones {
		if k := phoneMatchKey(p); k != "" {
			keys = append(keys, "p:"+k)
		}
	}
	for _, e := range uc.Emails {
		keys = append(keys, "e:"+normEmail(e))
	}
	if len(keys) == 0 {
		keys = append(keys, "n:"+normName(uc.Name))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return "u_" + hex.EncodeToString(sum[:8])
}

// ─── union-find ────────────────────────────────────────────────────────────────

type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return &unionFind{parent: p}
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]] // path compression
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
