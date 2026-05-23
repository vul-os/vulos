// SearchBar.js — AIROT-04: Enhanced search bar with semantic search support.
//
// Augments the existing full-text note search with semantic (AI) search.
// Calls POST /api/ai/notes/search; gracefully degrades to full-text only when
// the AI Router is unconfigured or returns an error.
//
// Usage:
//   import { semanticSearch, mergeResults } from './SearchBar.js';
//   const { hits, degraded } = await semanticSearch(query);
//   const merged = mergeResults(allNotes, hits);

/**
 * Perform a semantic search over indexed notes.
 *
 * @param {string} query
 * @param {{ threshold?: number, topK?: number }} [opts]
 * @returns {Promise<{ hits: Array<{note_id: string, score: number}>, degraded: boolean }>}
 */
export async function semanticSearch(query, opts = {}) {
  const payload = {
    query,
    threshold: opts.threshold ?? 0.75,
    top_k: opts.topK ?? 5,
  };
  try {
    const res = await fetch('/api/ai/notes/search', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) {
      // AI Router not configured (503) or other error — degrade gracefully.
      return { hits: [], degraded: true };
    }
    const data = await res.json();
    return {
      hits: Array.isArray(data.hits) ? data.hits : [],
      degraded: !!data.degraded,
    };
  } catch (_) {
    return { hits: [], degraded: true };
  }
}

/**
 * Merge full-text note list with semantic hits.
 *
 * Semantic hits that are already in `allNotes` get an `aiMatch` flag and are
 * surfaced first (in score order). Full-text matches follow.
 *
 * @param {Array<{id: string, title: string, preview: string}>} allNotes
 * @param {Array<{note_id: string, score: number}>} semanticHits
 * @param {string} query  - used for full-text filter
 * @returns {Array<{id: string, title: string, preview: string, aiMatch?: boolean, aiScore?: number}>}
 */
export function mergeResults(allNotes, semanticHits, query) {
  const lq = query.toLowerCase();
  const semanticMap = new Map(semanticHits.map(h => [h.note_id, h.score]));

  // Full-text matches (existing behaviour).
  const ftMatches = lq
    ? allNotes.filter(
        n =>
          n.title.toLowerCase().includes(lq) ||
          n.preview.toLowerCase().includes(lq)
      )
    : allNotes;

  const seen = new Set();

  // 1. Semantic hits first (AI search badge).
  const semanticResults = [];
  for (const hit of semanticHits) {
    const note = allNotes.find(n => n.id === hit.note_id);
    if (note) {
      seen.add(note.id);
      semanticResults.push({ ...note, aiMatch: true, aiScore: hit.score });
    }
  }

  // 2. Full-text results that are not already shown as semantic hits.
  const ftResults = ftMatches
    .filter(n => !seen.has(n.id))
    .map(n => ({ ...n, aiMatch: false }));

  return [...semanticResults, ...ftResults];
}
