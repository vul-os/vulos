// aiIndex.js — AIROT-04: Notes AI indexing via the OS AI Router.
//
// Background-indexes note content by calling POST /api/ai/notes/index on the
// OS local server (airouter package). Silently degrades when the AI Router is
// not configured (503 / network error).
//
// Usage:
//   import { enqueueIndex, cancelIndex } from './aiIndex.js';
//   enqueueIndex(noteId, content);   // fire-and-forget after save
//   cancelIndex(noteId);             // optional: abort in-flight request

/** @type {Map<string, AbortController>} */
const _pending = new Map();

/**
 * Enqueue a background embedding request for a note.
 * If there is already a pending request for the same noteId it is cancelled
 * first (debounce: only the latest save matters).
 *
 * @param {string} noteId
 * @param {string} content
 * @returns {void}
 */
export function enqueueIndex(noteId, content) {
  // Cancel any in-flight request for this note.
  cancelIndex(noteId);

  const ctrl = new AbortController();
  _pending.set(noteId, ctrl);

  _doIndex(noteId, content, ctrl.signal).finally(() => {
    if (_pending.get(noteId) === ctrl) {
      _pending.delete(noteId);
    }
  });
}

/**
 * Cancel a pending index request for noteId (no-op if none).
 * @param {string} noteId
 */
export function cancelIndex(noteId) {
  const ctrl = _pending.get(noteId);
  if (ctrl) {
    ctrl.abort();
    _pending.delete(noteId);
  }
}

/**
 * Internal: perform the embed+store call. Errors are swallowed so the notes
 * app continues to function when the AI Router is unconfigured.
 *
 * @param {string} noteId
 * @param {string} content
 * @param {AbortSignal} signal
 */
async function _doIndex(noteId, content, signal) {
  try {
    const res = await fetch('/api/ai/notes/index', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ note_id: noteId, content }),
      signal,
    });
    if (!res.ok && res.status !== 503) {
      // 503 = AI Router not configured; silent degrade.
      // Other errors: log for diagnostics but don't surface to the user.
      const body = await res.text().catch(() => '');
      console.warn('[aiIndex] index failed', res.status, body);
    }
  } catch (err) {
    if (err.name !== 'AbortError') {
      console.warn('[aiIndex] index error (AI Router may be unconfigured):', err.message);
    }
  }
}

/**
 * Remove the stored embedding for a deleted note.
 * Fire-and-forget; errors are swallowed.
 *
 * @param {string} noteId
 */
export function removeIndex(noteId) {
  cancelIndex(noteId);
  fetch('/api/ai/notes/' + encodeURIComponent(noteId) + '/embed', {
    method: 'DELETE',
  }).catch(() => {});
}
