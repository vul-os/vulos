import { useEffect, useState, useCallback, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useEditor, EditorContent, Extension } from '@tiptap/react'
import { Plugin, PluginKey } from '@tiptap/pm/state'
import { Decoration, DecorationSet } from '@tiptap/pm/view'
import StarterKit from '@tiptap/starter-kit'
import Image from '@tiptap/extension-image'
import Link from '@tiptap/extension-link'
import TextStyle from '@tiptap/extension-text-style'
import Color from '@tiptap/extension-color'
import Highlight from '@tiptap/extension-highlight'
import Underline from '@tiptap/extension-underline'
import TextAlign from '@tiptap/extension-text-align'
import Table from '@tiptap/extension-table'
import TableRow from '@tiptap/extension-table-row'
import TableCell from '@tiptap/extension-table-cell'
import TableHeader from '@tiptap/extension-table-header'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import CharacterCount from '@tiptap/extension-character-count'
import Placeholder from '@tiptap/extension-placeholder'
import Typography from '@tiptap/extension-typography'
import { ArrowLeft, Save, Loader2, AlertCircle, History, Users, MessageSquare, Activity, GitBranch, Check, Circle, Search, Type as TypeIcon } from 'lucide-react'
import FindReplace from './components/FindReplace'
import WordCountModal from './components/WordCountModal'
import { useFilesStore, getSaveState, onSaveStateChange } from '../../store/filesStore'
import { api } from '../../lib/api'
import { readDraft, clearDraft } from '../../lib/draftStore'
import DocsToolbar from './DocsToolbar'
import HistoryPanel from '../../components/HistoryPanel'
import CommentsPanel from '../../components/CommentsPanel'
import SuggestionPanel from '../../components/SuggestionPanel'
import ActivityFeed from '../../components/ActivityFeed'
import { DocsCollabSession } from '../../lib/crdt/index.js'
import { getSuggestionStore } from '../../lib/crdt/suggestions.js'
import { useLiveCursors } from '../../lib/useLiveCursors.js'
import { DocsCursorLayer } from '../../components/RemoteCursors.jsx'
import { Button, IconButton, Tooltip, Topbar } from '../../components/ui'

// Imported files may carry _html; use that as editor content
function resolveContent(content) {
  if (!content) return { type: 'doc', content: [{ type: 'paragraph' }] }
  if (content._html) return content._html  // TipTap accepts HTML string
  return content
}

const RETRY_DELAY_MS = 4000
const AUTOSAVE_DELAY_MS = 2000

// ---------------------------------------------------------------------------
// applyTextPatch — apply a remote CRDT text change to a TipTap editor
// without clobbering the local caret position.
//
// Strategy: locate the changed region (common prefix/suffix), then issue
// TipTap deleteRange + insertContentAt so only the changed characters are
// touched. This keeps the caret stable for insertions and deletions outside
// the user's current cursor region.
// ---------------------------------------------------------------------------
function applyTextPatch(editor, prevText, nextText) {
  if (prevText === nextText) return

  // Find common prefix.
  let pre = 0
  while (pre < prevText.length && pre < nextText.length && prevText[pre] === nextText[pre]) pre++

  // Find common suffix (not overlapping prefix).
  let suf = 0
  while (
    suf < prevText.length - pre &&
    suf < nextText.length - pre &&
    prevText[prevText.length - 1 - suf] === nextText[nextText.length - 1 - suf]
  ) suf++

  const deleteCount = prevText.length - pre - suf
  const insertStr = nextText.slice(pre, nextText.length - suf)

  // TipTap positions are 1-based (the doc node itself occupies position 0).
  // getText() returns all chars with no extra delimiters, but character
  // offset in the doc may differ from string offset in multi-node docs.
  // For a plain-text approximation we use from=pre+1 which is valid for
  // simple single-paragraph docs; for richly-structured docs this is a
  // best-effort reconcile.
  const from = pre + 1
  const to = from + deleteCount

  editor.chain()
    .deleteRange({ from, to })
    .insertContentAt(from, insertStr)
    .run()
}

// Derive a stable peerId for this browser session (persists across reloads).
function getOrCreatePeerId() {
  let id = sessionStorage.getItem('vulos_peer_id')
  if (!id) {
    id = crypto.randomUUID()
    sessionStorage.setItem('vulos_peer_id', id)
  }
  return id
}

// ---------------------------------------------------------------------------
// OFFICE-27: SuggestionDecorations — ProseMirror plugin that renders pending
// suggestion annotations as inline highlights (green = insert, red strikethrough
// = delete).  The plugin state is a plain array updated via a custom transaction
// meta key whenever suggestions change.
// ---------------------------------------------------------------------------

const SUGGESTION_PLUGIN_KEY = new PluginKey('suggestions')

function makeSuggestionDecoration(from, to, kind) {
  // TipTap/ProseMirror positions are 1-indexed and include node boundaries.
  // Character offset `from` in plain text ≈ doc position `from + 1`.
  const docFrom = from + 1
  const docTo = to + 1
  if (kind === 'insert') {
    // Inline widget at cursor position showing the inserted text.
    return Decoration.inline(docFrom, Math.max(docTo, docFrom + 1), {
      class: 'suggestion-insert',
    })
  } else {
    return Decoration.inline(docFrom, Math.max(docTo, docFrom + 1), {
      class: 'suggestion-delete',
    })
  }
}

function buildSuggestionPlugin() {
  return new Plugin({
    key: SUGGESTION_PLUGIN_KEY,
    state: {
      init() { return { suggestions: [], decorations: DecorationSet.empty } },
      apply(tr, old, _oldState, newState) {
        const meta = tr.getMeta(SUGGESTION_PLUGIN_KEY)
        if (meta) {
          const pending = (meta.suggestions || []).filter((s) => s.state === 'pending')
          const decos = pending.flatMap((s) => {
            try {
              return [makeSuggestionDecoration(s.from, s.to, s.kind)]
            } catch { return [] }
          })
          return {
            suggestions: meta.suggestions,
            decorations: DecorationSet.create(newState.doc, decos),
          }
        }
        // Map existing decorations through document changes.
        return { suggestions: old.suggestions, decorations: old.decorations.map(tr.mapping, tr.doc) }
      },
    },
    props: {
      decorations(state) {
        return this.getState(state).decorations
      },
    },
  })
}

const SuggestionDecorationsExtension = Extension.create({
  name: 'suggestionDecorations',
  addProseMirrorPlugins() {
    return [buildSuggestionPlugin()]
  },
})

export default function DocsEditor() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { files, saveFileWithDraft, markDirty } = useFilesStore()
  const [file, setFile] = useState(files.find((f) => f.id === id))
  const [title, setTitle] = useState(file?.name || 'Untitled')
  const [pendingContent, setPendingContent] = useState(null)
  const [saveStatus, setSaveStatus] = useState(getSaveState(id))
  const [draft, setDraft] = useState(null)           // pending draft to offer restore
  const [retryCount, setRetryCount] = useState(0)
  const [showHistory, setShowHistory] = useState(false)
  const [showComments, setShowComments] = useState(false)
  const [showActivity, setShowActivity] = useState(false)
  // Find/Replace
  const [findMode, setFindMode] = useState(null) // null | 'find' | 'replace'
  // Word count modal
  const [showWordCount, setShowWordCount] = useState(false)
  // Page count (debounced)
  const [pageCount, setPageCount] = useState(1)
  // OFFICE-27: suggestion / track-changes mode
  const [suggestionMode, setSuggestionMode] = useState(false)
  const [showSuggestions, setShowSuggestions] = useState(false)
  const [suggestions, setSuggestions] = useState([])
  const suggestionModeRef = useRef(false)
  const prevTextForSugRef = useRef('')   // plain text before the suggestion-mode edit
  const [collabPeers, setCollabPeers] = useState({})  // peerId → state
  const saveTimer = useRef(null)
  const retryTimer = useRef(null)
  const titleRef = useRef(title)
  titleRef.current = title

  // CRDT collab session (OFFICE-22)
  const collabRef = useRef(null)
  // Tracks the plain text the CRDT last saw so we can diff on next local edit.
  const prevCrdtTextRef = useRef('')
  // Flag: true while we're applying a remote op so onUpdate doesn't re-broadcast.
  const applyingRemoteRef = useRef(false)

  // Subscribe to save state changes for this file
  useEffect(() => {
    const unsub = onSaveStateChange(id, (state) => setSaveStatus({ ...state }))
    return unsub
  }, [id])

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ heading: { levels: [1, 2, 3, 4] } }),
      Image.configure({ allowBase64: true }),
      Link.configure({ openOnClick: false }),
      TextStyle,
      Color,
      Highlight.configure({ multicolor: true }),
      Underline,
      TextAlign.configure({ types: ['heading', 'paragraph'] }),
      Table.configure({ resizable: true }),
      TableRow,
      TableCell,
      TableHeader,
      TaskList,
      TaskItem.configure({ nested: true }),
      CharacterCount,
      Placeholder.configure({ placeholder: 'Start writing…' }),
      Typography,
      // OFFICE-27: inline suggestion decorations (green insert / red strikethrough delete)
      SuggestionDecorationsExtension,
    ],
    content: resolveContent(file?.content),
    onUpdate: ({ editor: ed }) => {
      // ── OFFICE-27: suggestion mode intercept ──────────────────────────────
      // When suggestion mode is active: undo the edit, compute the diff, and
      // record it as a pending suggestion instead of applying it directly.
      if (suggestionModeRef.current && !applyingRemoteRef.current) {
        const nextText = ed.getText()
        const prevText = prevTextForSugRef.current
        if (nextText !== prevText) {
          // Revert the edit so the base document stays unchanged.
          ed.commands.undo()
          // Compute diff: common prefix/suffix → insert or delete range.
          let pre = 0
          while (pre < prevText.length && pre < nextText.length && prevText[pre] === nextText[pre]) pre++
          let suf = 0
          const maxSuf = Math.min(prevText.length - pre, nextText.length - pre)
          while (suf < maxSuf && prevText[prevText.length - 1 - suf] === nextText[nextText.length - 1 - suf]) suf++
          const deleted = prevText.slice(pre, suf > 0 ? prevText.length - suf : prevText.length)
          const inserted = nextText.slice(pre, suf > 0 ? nextText.length - suf : nextText.length)
          const from = pre
          const to = pre + deleted.length

          const peerId = sessionStorage.getItem('vulos_peer_id') || 'local'
          const store = getSuggestionStore(id)
          let sg
          if (inserted.length > 0) {
            sg = store.addInsert(from, to, inserted, peerId)
          } else if (deleted.length > 0) {
            sg = store.addDelete(from, to, peerId)
          }
          if (sg) {
            // Persist to backend (fire-and-forget; store already has it)
            api.createSuggestion(id, sg.kind, sg.author_id, sg.from, sg.to, sg.text || '').catch(() => {})
            setSuggestions(store.list())
            setShowSuggestions(true)
          }
        }
        return
      }

      markDirty(id)
      clearTimeout(saveTimer.current)
      clearTimeout(retryTimer.current)
      setRetryCount(0)
      saveTimer.current = setTimeout(() => doSave(), AUTOSAVE_DELAY_MS)

      // CRDT broadcast: skip if this update was triggered by a remote apply.
      if (!applyingRemoteRef.current && collabRef.current) {
        const nextText = ed.getText()
        collabRef.current.applyLocal(prevCrdtTextRef.current, nextText)
        prevCrdtTextRef.current = nextText
      }
    },
    onSelectionUpdate: ({ editor: ed }) => {
      // OFFICE-25: broadcast local caret/selection position to peers.
      if (broadcastDocCursorRef.current) {
        const { from, to } = ed.state.selection
        broadcastDocCursorRef.current(from, to)
      }
    },
  })

  // Load file from API if not in store cache
  useEffect(() => {
    if (!file && id) {
      api.getFile(id).then((f) => {
        setFile(f)
        setTitle(f.name)
        setPendingContent(resolveContent(f.content))
      }).catch(() => navigate('/docs'))
    }
  }, [id])

  // Check for a pending draft on mount (crash recovery)
  useEffect(() => {
    if (!id) return
    readDraft(id).then((d) => {
      if (d && d.ts) {
        setDraft(d)
      }
    })
  }, [id])

  // Apply pending content once editor is ready
  useEffect(() => {
    if (editor && pendingContent !== null) {
      editor.commands.setContent(pendingContent, false)
      setPendingContent(null)
    }
  }, [editor, pendingContent])

  // ── CRDT collab session (OFFICE-22) ──────────────────────────────────────
  useEffect(() => {
    if (!id) return

    const peerId = getOrCreatePeerId()
    const session = new DocsCollabSession({ fileId: id, peerId })
    collabRef.current = session

    // Remote-change handler: apply peer op to editor without caret jump.
    session.addEventListener('change', (ev) => {
      if (!ev.detail.remote) return
      const remoteText = ev.detail.text
      // Guard: don't re-broadcast this programmatic update.
      applyingRemoteRef.current = true
      try {
        // We reconcile by replacing editor content only when the plain text
        // has actually diverged.  We preserve the HTML structure by doing a
        // character-level merge instead of a full setContent replacement:
        // for plain-text divergence we fall back to setContent on the
        // current JSON with the text patched.  The common case (single char
        // insert/delete) is handled via TipTap's insertContentAt / deleteRange
        // so the caret stays stable.
        //
        // For simplicity in V1 we use a safe full-replace only when the
        // texts differ, keeping the existing JSON structure otherwise.
        // This covers the no-caret-jump requirement for small edits.
        if (editorRef.current) {
          const ed = editorRef.current
          const curText = ed.getText()
          if (curText !== remoteText) {
            // Build a minimal patch: find common prefix/suffix and apply
            // TipTap commands to sync the plain-text range that changed.
            applyTextPatch(ed, curText, remoteText)
            prevCrdtTextRef.current = ed.getText()
          }
        }
      } finally {
        applyingRemoteRef.current = false
      }
    })

    // Peer connection-state events (for optional UI indicator).
    session.addEventListener('state', (ev) => {
      const { peerId: pid, state } = ev.detail
      setCollabPeers((prev) => ({ ...prev, [pid]: state }))
    })

    // Async join — errors are non-fatal (signaling may be unavailable
    // in single-user / offline mode; local editing continues normally).
    session.join().catch((err) => {
      console.warn('[collab] fabric join failed (single-user mode):', err?.message)
    })

    return () => {
      session.leave()
      collabRef.current = null
    }
  }, [id])

  // Keep a ref to the editor instance for use inside the collab event handler.
  const editorRef = useRef(null)
  useEffect(() => {
    editorRef.current = editor
  }, [editor])

  // ── OFFICE-27: Suggestion mode ────────────────────────────────────────────

  // Keep ref in sync so the onUpdate closure (created once) always reads the latest value.
  useEffect(() => {
    suggestionModeRef.current = suggestionMode
    if (editor) prevTextForSugRef.current = editor.getText()
  }, [suggestionMode, editor])

  // Load suggestions from server on mount.
  useEffect(() => {
    if (!id) return
    api.listSuggestions(id)
      .then((items) => {
        const store = getSuggestionStore(id)
        store.loadFromServer(items || [])
        setSuggestions(store.list())
      })
      .catch(() => {}) // backend may not be running — fail silently
  }, [id])

  // Toggle suggestion mode: update ref + seed prevTextForSug.
  const handleToggleSuggestionMode = () => {
    setSuggestionMode((v) => {
      const next = !v
      suggestionModeRef.current = next
      if (editor) prevTextForSugRef.current = editor.getText()
      if (next) setShowSuggestions(true)
      return next
    })
  }

  // Accept: apply the change to the document and mark accepted.
  const handleAcceptSuggestion = async (sg) => {
    if (!editor) return
    applyingRemoteRef.current = true
    try {
      if (sg.kind === 'insert') {
        // Insert the suggested text at the proposed offset (from == to for pure insert).
        editor.chain().focus().insertContentAt(sg.from + 1, sg.text).run()
      } else {
        // Delete the proposed range.
        editor.chain().focus().deleteRange({ from: sg.from + 1, to: sg.to + 1 }).run()
      }
      doSave()
    } finally {
      applyingRemoteRef.current = false
    }
    // Update store and backend.
    const store = getSuggestionStore(id)
    store.accept(sg.id, 'reviewer')
    setSuggestions(store.list())
    api.updateSuggestion(id, sg.id, 'accepted', 'reviewer').catch(() => {})
  }

  // Reject: discard the suggestion (document unchanged).
  const handleRejectSuggestion = async (sg) => {
    const store = getSuggestionStore(id)
    store.reject(sg.id, 'reviewer')
    setSuggestions(store.list())
    api.updateSuggestion(id, sg.id, 'rejected', 'reviewer').catch(() => {})
  }

  // Update ProseMirror decorations whenever the suggestions list changes.
  useEffect(() => {
    if (!editor) return
    try {
      const { tr } = editor.state
      tr.setMeta(SUGGESTION_PLUGIN_KEY, { suggestions })
      editor.view.dispatch(tr)
    } catch {
      // Editor may not be ready; decorations will be applied on next update.
    }
  }, [suggestions, editor])

  // ── OFFICE-25: Live cursors ───────────────────────────────────────────────
  // Derive a stable local identity from sessionStorage (mirrors CRDT peerId approach).
  const localCursorIdentity = useRef(null)
  if (!localCursorIdentity.current) {
    try {
      const stored = localStorage.getItem('presence_identity')
      const parsed = stored ? JSON.parse(stored) : null
      localCursorIdentity.current = parsed && parsed.accountId ? parsed : {
        accountId: `guest:${sessionStorage.getItem('vulos_peer_id') || 'local'}`,
        displayName: 'Me',
      }
    } catch { localCursorIdentity.current = { accountId: 'local', displayName: 'Me' } }
  }
  // Expose fabric from collab session for cursor transport.
  const [fabricForCursors, setFabricForCursors] = useState(null)
  useEffect(() => {
    // collabRef.current is set inside the CRDT effect above; check after it runs.
    const check = () => setFabricForCursors(collabRef.current?.fabric ?? null)
    // Give the collab effect a tick to run first.
    const t = setTimeout(check, 100)
    return () => clearTimeout(t)
  }, [id])

  const { remoteCursors, broadcastDocCursor } = useLiveCursors({
    fabric: fabricForCursors,
    localIdentity: localCursorIdentity.current,
    color: localCursorIdentity.current
      ? (() => { let h=0; for(const c of localCursorIdentity.current.accountId){h=(h<<5)-h+c.charCodeAt(0);h|=0} return `hsl(${Math.abs(h)%360},65%,50%)` })()
      : '#6366f1',
  })
  // Stable ref so the useEditor onSelectionUpdate closure can call the latest version.
  const broadcastDocCursorRef = useRef(null)
  broadcastDocCursorRef.current = broadcastDocCursor

  // ── Keyboard shortcuts ────────────────────────────────────────────────────
  useEffect(() => {
    const onKey = (e) => {
      const mod = e.metaKey || e.ctrlKey
      if (!mod) return
      if (e.key === 'f' || e.key === 'F') {
        e.preventDefault()
        setFindMode((m) => (m ? null : 'find'))
        return
      }
      if (e.key === 'h' || e.key === 'H') {
        e.preventDefault()
        setFindMode((m) => (m === 'replace' ? null : 'replace'))
        return
      }
      if (e.key === 'k' || e.key === 'K') {
        // Handled by TipTap shortcut, but ensure link popover is triggered
        // if editor has text selected — nothing to override here since TipTap's
        // Link extension binds Cmd+K by default.
        return
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])

  // ── Page count (debounced update every 200ms) ─────────────────────────────
  const pageCountTimer = useRef(null)
  useEffect(() => {
    if (!editor) return
    const update = () => {
      clearTimeout(pageCountTimer.current)
      pageCountTimer.current = setTimeout(() => {
        const words = editor.storage.characterCount?.words() ?? 0
        setPageCount(Math.max(1, Math.ceil(words / WORDS_PER_PAGE)))
      }, 200)
    }
    editor.on('update', update)
    update() // initial
    return () => {
      editor.off('update', update)
      clearTimeout(pageCountTimer.current)
    }
  }, [editor])

  const doSave = useCallback(async (retryNum = 0) => {
    if (!editor || !id) return
    try {
      await saveFileWithDraft(id, titleRef.current, editor.getJSON())
      setRetryCount(0)
    } catch {
      // Schedule retry with backoff (up to 3 retries)
      if (retryNum < 3) {
        const delay = RETRY_DELAY_MS * (retryNum + 1)
        retryTimer.current = setTimeout(() => {
          setRetryCount(retryNum + 1)
          doSave(retryNum + 1)
        }, delay)
      }
    }
  }, [editor, id, saveFileWithDraft])

  const handleSave = () => {
    clearTimeout(saveTimer.current)
    clearTimeout(retryTimer.current)
    setRetryCount(0)
    doSave()
  }

  const handleTitleChange = (newTitle) => {
    setTitle(newTitle)
    markDirty(id)
    clearTimeout(saveTimer.current)
    saveTimer.current = setTimeout(() => doSave(), 1500)
  }

  const handleRestoreDraft = () => {
    if (!draft || !editor) return
    editor.commands.setContent(resolveContent(draft.content), false)
    if (draft.name) setTitle(draft.name)
    setDraft(null)
    markDirty(id)
  }

  const handleDiscardDraft = () => {
    clearDraft(id)
    setDraft(null)
  }

  const wordCount = editor?.storage.characterCount?.words() ?? 0
  const charCount = editor?.storage.characterCount?.characters() ?? 0
  // Estimate page count (debounced in onUpdate, mirrors WordCountModal logic)
  const WORDS_PER_PAGE = 250

  // Discreet save status — a meta-line, never a banner.
  // We render an icon + text inline with the title; colour is intentionally
  // muted (the user shouldn't keep scanning to see "Saved" all the time).
  const statusInfo = (() => {
    switch (saveStatus.status) {
      case 'saving':
        return { text: 'Saving',  tone: 'muted',   icon: Loader2,     spin: true  }
      case 'saved':
        return { text: 'Saved',   tone: 'success', icon: Check,       spin: false }
      case 'error':
        return {
          text: retryCount > 0 ? `Retrying ${retryCount}/3` : 'Save failed',
          tone: 'danger',
          icon: AlertCircle,
          spin: false,
        }
      case 'dirty':
        return { text: 'Unsaved', tone: 'muted',   icon: Circle,      spin: false }
      default:
        return null
    }
  })()

  if (!editor) {
    return (
      <div className="flex-1 flex items-center justify-center bg-bg">
        <Loader2 className="animate-spin text-accent" size={22} />
      </div>
    )
  }

  const peerCount = Object.values(collabPeers)
    .filter((s) => s === 'connected' || s === 'relay').length
  const pendingSuggestions = suggestions.filter((s) => s.state === 'pending').length
  const StatusIcon = statusInfo?.icon

  return (
    <div className="flex-1 flex flex-col overflow-hidden bg-bg">
      {/* Draft-restore banner — only banner we keep, because it requires action */}
      {draft && (
        <div className="flex items-center gap-3 px-4 py-2 bg-warning-bg border-b border-line text-xs text-warning animate-fade-in">
          <AlertCircle size={14} className="flex-shrink-0" />
          <span className="flex-1 text-ink-muted">
            Unsaved changes from a previous session were found.
          </span>
          <Button variant="primary" size="sm" onClick={handleRestoreDraft}>Restore</Button>
          <Button variant="secondary" size="sm" onClick={handleDiscardDraft}>Discard</Button>
        </div>
      )}

      {/*
        Save errors are reported in the meta-line of the topbar (statusInfo);
        we deliberately do NOT show a big red banner — that's a Mercury-style
        restraint: errors stay surfaced, never alarming, with action via Save.
      */}

      {/* Top bar — composed from the design system */}
      <Topbar
        leading={
          <Tooltip label="Back to Docs">
            <IconButton size="sm" onClick={() => navigate('/docs')}>
              <ArrowLeft size={15} />
            </IconButton>
          </Tooltip>
        }
        title={
          <input
            value={title}
            onChange={(e) => handleTitleChange(e.target.value)}
            placeholder="Untitled document"
            className={[
              'flex-1 min-w-0 text-sm font-semibold tracking-tightish',
              'bg-transparent border border-transparent rounded-sm px-2 py-1',
              'text-ink placeholder:text-ink-faint',
              'hover:border-line focus:border-line-strong focus:bg-paper',
              'transition-[border-color,background] duration-fast ease-out outline-none',
            ].join(' ')}
          />
        }
        meta={
          <>
            {statusInfo && (
              <span
                className={[
                  'inline-flex items-center gap-1.5 px-1.5 py-0.5 rounded-sm',
                  statusInfo.tone === 'success' ? 'text-success' :
                  statusInfo.tone === 'danger'  ? 'text-danger' :
                                                  'text-ink-faint',
                ].join(' ')}
                title={saveStatus.error || ''}
              >
                {StatusIcon && (
                  <StatusIcon
                    size={11}
                    className={statusInfo.spin ? 'animate-spin' : ''}
                  />
                )}
                {statusInfo.text}
              </span>
            )}
            {peerCount > 0 && (
              <span
                className="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-pill bg-accent-tint text-accent-press"
                title={`${peerCount} peer(s) connected`}
              >
                <Users size={11} />
                {peerCount}
              </span>
            )}
          </>
        }
        actions={
          <>
            <Tooltip label="Find (Cmd+F)">
              <IconButton size="sm" active={!!findMode} onClick={() => setFindMode((m) => (m ? null : 'find'))}>
                <Search size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Version history">
              <IconButton size="sm" active={showHistory} onClick={() => setShowHistory((v) => !v)}>
                <History size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Activity">
              <IconButton size="sm" active={showActivity} onClick={() => setShowActivity((v) => !v)}>
                <Activity size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label="Comments">
              <IconButton size="sm" active={showComments} onClick={() => setShowComments((v) => !v)}>
                <MessageSquare size={14} />
              </IconButton>
            </Tooltip>
            <Tooltip label={suggestionMode ? 'Exit suggestion mode' : 'Suggestion mode (track changes)'}>
              <button
                onClick={handleToggleSuggestionMode}
                className={[
                  'inline-flex items-center gap-1 h-7 px-2 rounded-md text-xs font-medium',
                  'tracking-tightish transition-colors duration-fast ease-out',
                  suggestionMode
                    ? 'bg-success-bg text-success border border-success'
                    : 'text-ink-muted hover:bg-accent-tint hover:text-ink',
                ].join(' ')}
              >
                <GitBranch size={13} />
                {suggestionMode ? 'Suggesting' : 'Suggest'}
              </button>
            </Tooltip>
            {!suggestionMode && pendingSuggestions > 0 && (
              <Tooltip label={`${pendingSuggestions} pending suggestion${pendingSuggestions === 1 ? '' : 's'}`}>
                <IconButton size="sm" active={showSuggestions} onClick={() => setShowSuggestions((v) => !v)}>
                  <GitBranch size={14} />
                </IconButton>
              </Tooltip>
            )}
            <Button
              variant="primary"
              size="sm"
              onClick={handleSave}
              disabled={saveStatus.status === 'saving'}
            >
              {saveStatus.status === 'saving'
                ? <Loader2 size={13} className="animate-spin" />
                : <Save size={13} />}
              Save
            </Button>
          </>
        }
      />

      {/* Suggestion-mode hint strip — quiet, not alarming */}
      {suggestionMode && (
        <div className="flex items-center gap-2 px-4 py-1.5 bg-success-bg border-b border-line text-xs text-success animate-fade-in">
          <GitBranch size={12} className="flex-shrink-0" />
          <span className="flex-1 text-ink-muted tracking-tightish">
            Suggestion mode — edits are recorded as proposals.
          </span>
          <Button variant="ghost" size="sm" onClick={() => setShowSuggestions((v) => !v)}>
            {showSuggestions ? 'Hide' : 'Review'}
          </Button>
        </div>
      )}

      <DocsToolbar editor={editor} title={title} />

      {/* Editor canvas + side panels */}
      <div className="flex-1 flex overflow-hidden bg-bg">
        {/*
          Page canvas — the document feels like paper:
            - warm paper background under a measured (~720px) writing column
            - generous side margins and vertical padding
            - subtle 1-px line on the page edge, no heavy shadow
            - .paper-grain adds a near-imperceptible letterpress tooth
        */}
        <div className="flex-1 overflow-auto px-6 py-10 relative">
          {/* Find/Replace floating bar */}
          {findMode && (
            <FindReplace
              editor={editor}
              mode={findMode}
              onClose={() => setFindMode(null)}
            />
          )}
          <article
            className="paper-grain mx-auto bg-paper border border-line rounded-lg shadow-e1 px-14 py-16"
            style={{ maxWidth: '760px' }}
          >
            <div className="tiptap-cursor-host relative animate-fade-in">
              <EditorContent editor={editor} className="tiptap" />
              <DocsCursorLayer editor={editor} remoteCursors={remoteCursors} />
            </div>
          </article>
        </div>

        {/* History panel (OFFICE-08) */}
        {showHistory && (
          <HistoryPanel
            fileId={id}
            onClose={() => setShowHistory(false)}
            onRestore={(updated) => {
              if (editor && updated?.content) {
                editor.commands.setContent(resolveContent(updated.content), false)
              }
              if (updated?.name) setTitle(updated.name)
            }}
          />
        )}

        {/* Activity feed + named snapshots (OFFICE-28) */}
        {showActivity && (
          <ActivityFeed
            fileId={id}
            onClose={() => setShowActivity(false)}
            onRestore={(updated) => {
              if (editor && updated?.content) {
                editor.commands.setContent(resolveContent(updated.content), false)
              }
              if (updated?.name) setTitle(updated.name)
            }}
          />
        )}

        {/* Comments panel (OFFICE-26) */}
        {showComments && (
          <CommentsPanel
            fileId={id}
            anchorCtx={editor?.state?.selection
              ? {
                  type: 'text_range',
                  from: editor.state.selection.from,
                  to: editor.state.selection.to,
                  snapshot: editor.state.doc.textBetween(
                    editor.state.selection.from,
                    editor.state.selection.to,
                    ' '
                  ).slice(0, 80),
                }
              : null
            }
            onClose={() => setShowComments(false)}
          />
        )}

        {/* Suggestion panel (OFFICE-27) */}
        {showSuggestions && (
          <SuggestionPanel
            fileId={id}
            suggestions={suggestions}
            onAccept={handleAcceptSuggestion}
            onReject={handleRejectSuggestion}
            onClose={() => setShowSuggestions(false)}
          />
        )}
      </div>

      <footer className="flex items-center justify-end gap-4 px-4 h-7 bg-paper border-t border-line text-2xs text-ink-faint tracking-tightish">
        <button
          onClick={() => setShowWordCount(true)}
          title="Word count details"
          className="flex items-center gap-2 hover:text-ink transition-colors"
          aria-label="Open word count details"
        >
          <span>{wordCount} words</span>
          <span className="opacity-40">·</span>
          <span>{pageCount} {pageCount === 1 ? 'page' : 'pages'}</span>
          <span className="opacity-40">·</span>
          <span>{charCount} characters</span>
        </button>
      </footer>

      {/* Word count detail modal */}
      {showWordCount && (
        <WordCountModal editor={editor} onClose={() => setShowWordCount(false)} />
      )}
    </div>
  )
}
