/**
 * Vula OS — Notes Collaboration Layer (PEER-32)
 *
 * Self-contained Yjs collaboration for the Notes app.
 * - Loads Yjs + y-websocket from CDN (esm.sh)
 * - Connects to /api/peering/collab/:docId/sync via WebSocket
 * - Binds a Y.Text to the textarea, syncing edits for all peers
 * - Broadcasts cursor position (awareness) as ephemeral data
 * - Share button calls POST /api/peering/collab/share
 *
 * Usage (injected by index.html after a note is selected):
 *   window.collabSession = await startCollab(noteId, textarea, statusEl)
 *   window.collabSession.destroy()   // when switching notes
 */

/* ---------- Bootstrap: dynamic ESM import ---------- */
let _yjs = null;

async function loadYjs() {
  if (_yjs) return _yjs;
  // Import Yjs + y-websocket from CDN as ESM modules
  const [{ Doc, Text, applyUpdate, encodeStateAsUpdate }, { WebsocketProvider }] =
    await Promise.all([
      import('https://esm.sh/yjs@13.6.18'),
      import('https://esm.sh/y-websocket@1.5.4'),
    ]);
  _yjs = { Doc, Text, applyUpdate, encodeStateAsUpdate, WebsocketProvider };
  return _yjs;
}

/* ---------- Cursor colours (deterministic per clientId) ---------- */
const CURSOR_COLOURS = [
  '#3b82f6', '#ef4444', '#10b981', '#f59e0b',
  '#8b5cf6', '#ec4899', '#14b8a6', '#f97316',
];

function colourForClient(clientId) {
  return CURSOR_COLOURS[clientId % CURSOR_COLOURS.length];
}

/* ---------- Cursor overlay on top of the textarea ---------- */
class CursorOverlay {
  constructor(textarea) {
    this.textarea = textarea;
    this.cursors = {}; // clientId → {el, labelEl}

    // Mirror div that sits behind the textarea for measurement
    this.mirror = document.createElement('div');
    Object.assign(this.mirror.style, {
      position: 'absolute',
      top: '0', left: '0',
      visibility: 'hidden',
      whiteSpace: 'pre-wrap',
      wordBreak: 'break-word',
      font: getComputedStyle(textarea).font,
      padding: getComputedStyle(textarea).padding,
      width: textarea.offsetWidth + 'px',
      boxSizing: 'border-box',
    });
    document.body.appendChild(this.mirror);

    this.container = document.createElement('div');
    Object.assign(this.container.style, {
      position: 'absolute',
      pointerEvents: 'none',
      top: '0', left: '0',
      width: '100%', height: '100%',
      overflow: 'hidden',
      zIndex: '10',
    });
    // Position the container relative to the textarea's parent
    const parent = textarea.parentElement;
    parent.style.position = 'relative';
    parent.appendChild(this.container);
  }

  /** Returns {x, y} pixel coords for the caret at `index` in the textarea */
  coordsAt(index) {
    const ta = this.textarea;
    const text = ta.value;
    const before = text.slice(0, index);

    this.mirror.style.width = ta.offsetWidth + 'px';
    this.mirror.style.font = getComputedStyle(ta).font;
    this.mirror.style.padding = getComputedStyle(ta).padding;
    this.mirror.textContent = before;

    const span = document.createElement('span');
    span.textContent = '|';
    this.mirror.appendChild(span);
    document.body.appendChild(this.mirror); // re-attach if removed

    const taRect = ta.getBoundingClientRect();
    const spRect = span.getBoundingClientRect();
    this.mirror.removeChild(span);

    return {
      x: spRect.left - taRect.left,
      y: spRect.top - taRect.top + ta.scrollTop,
    };
  }

  updateCursor(clientId, index, name, colour) {
    if (!this.cursors[clientId]) {
      const el = document.createElement('div');
      Object.assign(el.style, {
        position: 'absolute',
        width: '2px',
        backgroundColor: colour,
        pointerEvents: 'none',
      });
      const label = document.createElement('div');
      Object.assign(label.style, {
        position: 'absolute',
        top: '-18px',
        left: '0',
        background: colour,
        color: '#fff',
        fontSize: '10px',
        padding: '1px 4px',
        borderRadius: '3px',
        whiteSpace: 'nowrap',
        pointerEvents: 'none',
      });
      label.textContent = name || 'Peer';
      el.appendChild(label);
      this.container.appendChild(el);
      this.cursors[clientId] = { el, label };
    }

    const { el } = this.cursors[clientId];
    const lineHeight = parseInt(getComputedStyle(this.textarea).lineHeight) || 20;
    try {
      const { x, y } = this.coordsAt(index);
      Object.assign(el.style, {
        left: x + 'px',
        top: y + 'px',
        height: lineHeight + 'px',
        display: 'block',
      });
    } catch (_) {
      el.style.display = 'none';
    }
  }

  removeCursor(clientId) {
    if (this.cursors[clientId]) {
      this.cursors[clientId].el.remove();
      delete this.cursors[clientId];
    }
  }

  destroy() {
    this.container.remove();
    this.mirror.remove();
  }
}

/* ---------- Main entry point ---------- */
/**
 * @param {string} noteId   — the note's ID (used as docId)
 * @param {HTMLTextAreaElement} textarea
 * @param {HTMLElement} statusEl  — status bar element
 * @param {string} displayName    — local user's display name
 * @returns {Promise<{destroy:Function}>}
 */
export async function startCollab(noteId, textarea, statusEl, displayName = 'Me') {
  const { Doc, WebsocketProvider } = await loadYjs();

  const ydoc = new Doc();
  const ytext = ydoc.getText('content');

  // Determine WebSocket URL — proxy through the same origin
  const wsProto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  // The Vula gateway proxies /api/peering/collab/:docId/sync as a WS endpoint
  const wsUrl = `${wsProto}//${location.host}/api/peering/collab/${noteId}/sync`;

  let synced = false;
  let destroying = false;

  const provider = new WebsocketProvider(wsUrl, noteId, ydoc, {
    connect: true,
    // y-websocket expects the room name as second param
  });

  const overlay = new CursorOverlay(textarea);

  // Set local user awareness
  const localClientId = ydoc.clientID;
  provider.awareness.setLocalStateField('user', {
    name: displayName,
    colour: colourForClient(localClientId),
    cursor: null,
  });

  /* ---- Sync ydoc ↔ textarea ---- */
  let ignoreTextareaEvent = false;
  let ignoreYjsObserver = false;

  // On first sync: initialise textarea from Y.Text if non-empty, else push textarea → Y.Text
  provider.on('sync', (isSynced) => {
    if (!isSynced || synced) return;
    synced = true;
    statusEl.textContent = 'Collab: connected';

    const remote = ytext.toString();
    if (remote.length > 0) {
      // Remote state wins on first connect
      ignoreYjsObserver = true;
      textarea.value = remote;
      ignoreYjsObserver = false;
    } else if (textarea.value.length > 0) {
      // Push local content to Y.Text
      ydoc.transact(() => {
        ytext.insert(0, textarea.value);
      });
    }
  });

  provider.on('status', ({ status: s }) => {
    if (destroying) return;
    if (s === 'disconnected') statusEl.textContent = 'Collab: offline';
    if (s === 'connecting')   statusEl.textContent = 'Collab: connecting…';
    if (s === 'connected')    statusEl.textContent = 'Collab: connected';
  });

  // Y.Text → textarea (remote edits arrive)
  ytext.observe((event) => {
    if (ignoreYjsObserver || destroying) return;
    const newVal = ytext.toString();
    if (textarea.value === newVal) return;

    // Preserve cursor position through the update
    const start = textarea.selectionStart;
    const end = textarea.selectionEnd;

    ignoreTextareaEvent = true;
    textarea.value = newVal;
    ignoreTextareaEvent = false;

    // Restore cursor (best-effort)
    const newLen = newVal.length;
    textarea.setSelectionRange(Math.min(start, newLen), Math.min(end, newLen));
  });

  // textarea → Y.Text (local edits)
  function onInput() {
    if (ignoreTextareaEvent || destroying) return;
    const newVal = textarea.value;
    const old = ytext.toString();
    if (newVal === old) return;

    // Compute diff (simple: find common prefix/suffix, replace middle)
    let s = 0;
    while (s < old.length && s < newVal.length && old[s] === newVal[s]) s++;
    let eOld = old.length - 1;
    let eNew = newVal.length - 1;
    while (eOld >= s && eNew >= s && old[eOld] === newVal[eNew]) { eOld--; eNew--; }

    const deleteCount = eOld - s + 1;
    const insertText = newVal.slice(s, eNew + 1);

    ydoc.transact(() => {
      if (deleteCount > 0) ytext.delete(s, deleteCount);
      if (insertText.length > 0) ytext.insert(s, insertText);
    });

    // Broadcast cursor position
    provider.awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(localClientId),
      cursor: textarea.selectionStart,
    });
  }

  // Cursor movement → broadcast
  function onSelectionChange() {
    if (destroying) return;
    provider.awareness.setLocalStateField('user', {
      name: displayName,
      colour: colourForClient(localClientId),
      cursor: textarea.selectionStart,
    });
  }

  textarea.addEventListener('input', onInput);
  textarea.addEventListener('keyup', onSelectionChange);
  textarea.addEventListener('mouseup', onSelectionChange);

  /* ---- Awareness: remote cursors ---- */
  function renderAwareness() {
    const states = provider.awareness.getStates();
    states.forEach((state, clientId) => {
      if (clientId === localClientId) return;
      const user = state.user;
      if (!user) return;
      const colour = user.colour || colourForClient(clientId);
      const name = user.name || 'Peer';
      const cursor = user.cursor;
      if (cursor != null) {
        overlay.updateCursor(clientId, cursor, name, colour);
      }
    });
    // Remove stale cursors
    const activeIds = new Set(provider.awareness.getStates().keys());
    Object.keys(overlay.cursors).forEach((id) => {
      if (!activeIds.has(Number(id))) overlay.removeCursor(Number(id));
    });
  }

  provider.awareness.on('change', renderAwareness);

  /* ---- Destroy ---- */
  function destroy() {
    destroying = true;
    textarea.removeEventListener('input', onInput);
    textarea.removeEventListener('keyup', onSelectionChange);
    textarea.removeEventListener('mouseup', onSelectionChange);
    provider.awareness.off('change', renderAwareness);
    provider.destroy();
    ydoc.destroy();
    overlay.destroy();
    statusEl.textContent = 'Ready';
  }

  return { destroy };
}

/* ---------- Share dialog ---------- */
export async function shareNote(noteId, noteTitle) {
  const peerId = prompt(
    `Share "${noteTitle || noteId}" with a peer.\n\nEnter their Vula ID or slug (e.g. alice.vulos.org):`
  );
  if (!peerId) return;

  const permission = confirm('Grant edit access? (Cancel = view-only)') ? 'edit' : 'view';

  try {
    const res = await fetch('/api/peering/collab/share', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        doc_id: noteId,
        doc_type: 'notes',
        title: noteTitle || noteId,
        peer_id: peerId,
        permission,
      }),
    });
    if (res.ok) {
      alert(`Shared with ${peerId} (${permission}). They will receive an invitation.`);
    } else {
      const err = await res.text();
      alert(`Share failed: ${err || res.statusText}`);
    }
  } catch (e) {
    alert(`Share failed: ${e.message}`);
  }
}
