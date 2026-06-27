// Drive.jsx — the Vulos Files (Drive) OS app: the canonical browser for a user's
// per-user Drive (the `drive/` area of their object bucket). It is a thin,
// session-authed client over the PHASE-1 Files control plane (/api/files/*):
// navigate the folder tree, upload/download bytes, create folders, move/rename/
// delete, share (user grants + expiring share links) and inspect versions.
//
// Storage-mode agnostic (open-core / standalone): uploads prefer a direct
// presigned PUT when the grant carries a URL (cloud), and otherwise fall back to
// the OS-mediated data plane (PUT/GET /api/files/content) so the app works with
// local-FS storage and no cloud. All access is ACL-gated server-side; this UI
// only ever surfaces what the session is authorized to see.

import { useState, useEffect, useCallback, useRef } from 'react'
import { request, rawFetch } from '../../lib/api'

// ── theme tokens ───────────────────────────────────────────────────────────
const T = {
  bg: 'var(--bg-base)',
  surface: 'var(--bg-surface)',
  elevated: 'var(--bg-elevated)',
  hover: 'var(--bg-hover)',
  selected: 'var(--bg-selected)',
  border: 'var(--border-default)',
  borderStrong: 'var(--border-strong)',
  text: 'var(--text-primary)',
  textDim: 'var(--text-tertiary)',
  textFaint: 'var(--text-faint)',
  accent: 'var(--accent)',
}

// ── tiny API surface over /api/files ────────────────────────────────────────
const filesApi = {
  list: (parent) => request(`/files/list?parent=${encodeURIComponent(parent || '')}`),
  sharedWithMe: () => request('/files/shared-with-me'),
  createFolder: (parent_id, name) =>
    request('/files/folder', { method: 'POST', body: JSON.stringify({ parent_id, name }) }),
  uploadGrant: (parent_id, name, content_type) =>
    request('/files/upload-grant', { method: 'POST', body: JSON.stringify({ parent_id, name, content_type }) }),
  downloadGrant: (node_id) =>
    request('/files/download-grant', { method: 'POST', body: JSON.stringify({ node_id }) }),
  commit: (node_id, size, content_type, etag) =>
    request('/files/commit', { method: 'POST', body: JSON.stringify({ node_id, size, content_type, etag }) }),
  move: (node_id, new_parent_id, new_name) =>
    request('/files/move', { method: 'POST', body: JSON.stringify({ node_id, new_parent_id, new_name }) }),
  remove: (node_id) =>
    request('/files/delete', { method: 'POST', body: JSON.stringify({ node_id }) }),
  versions: (node) => request(`/files/versions?node=${encodeURIComponent(node)}`),
  shares: (node) => request(`/files/shares?node=${encodeURIComponent(node)}`),
  share: (node_id, principal_id, role) =>
    request('/files/share', { method: 'POST', body: JSON.stringify({ node_id, principal_id, role }) }),
  unshare: (node_id, principal_id) =>
    request('/files/unshare', { method: 'POST', body: JSON.stringify({ node_id, principal_id }) }),
  links: (node) => request(`/files/share-links?node=${encodeURIComponent(node)}`),
  createLink: (node_id, role, ttl_seconds) =>
    request('/files/share-link', { method: 'POST', body: JSON.stringify({ node_id, role, ttl_seconds }) }),
  revokeLink: (token) =>
    request('/files/share-link/revoke', { method: 'POST', body: JSON.stringify({ token }) }),
}

// uploadOne: upload-grant → PUT bytes (direct presigned, else OS data plane) →
// commit. Returns the committed node.
async function uploadOne(parentId, file) {
  const { node, grant } = await filesApi.uploadGrant(parentId, file.name, file.type || 'application/octet-stream')
  if (grant && grant.type === 'presigned' && grant.url) {
    const res = await fetch(grant.url, {
      method: grant.method || 'PUT',
      body: file,
      headers: file.type ? { 'Content-Type': file.type } : {},
    })
    if (!res.ok) throw new Error(`upload failed (${res.status})`)
  } else {
    const res = await rawFetch(`/files/content?node=${encodeURIComponent(node.id)}`, {
      method: 'PUT',
      body: file,
      headers: file.type ? { 'Content-Type': file.type } : {},
    })
    if (!res.ok) throw new Error(`upload failed (${res.status})`)
  }
  await filesApi.commit(node.id, file.size, file.type || '', '')
  return node
}

// downloadOne: download-grant → fetch bytes (direct presigned, else OS data
// plane) → trigger a browser save.
async function downloadOne(node) {
  const { grant } = await filesApi.downloadGrant(node.id)
  let blob
  if (grant && grant.type === 'presigned' && grant.url) {
    const res = await fetch(grant.url)
    if (!res.ok) throw new Error(`download failed (${res.status})`)
    blob = await res.blob()
  } else {
    const res = await rawFetch(`/files/content?node=${encodeURIComponent(node.id)}`)
    if (!res.ok) throw new Error(`download failed (${res.status})`)
    blob = await res.blob()
  }
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = node.name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// ── formatting helpers ───────────────────────────────────────────────────────
function fmtSize(n) {
  if (!n) return '—'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${u[i]}`
}
function fmtDate(s) {
  if (!s) return ''
  const d = new Date(s)
  if (isNaN(d)) return ''
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}
function fileGlyph(node) {
  if (node.is_dir) return '📁'
  const ext = (node.name.split('.').pop() || '').toLowerCase()
  if (['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp'].includes(ext)) return '🖼'
  if (['mp4', 'mov', 'webm', 'mkv', 'avi'].includes(ext)) return '🎬'
  if (['mp3', 'wav', 'flac', 'ogg', 'm4a'].includes(ext)) return '🎵'
  if (['pdf'].includes(ext)) return '📕'
  if (['zip', 'tar', 'gz', '7z', 'rar'].includes(ext)) return '🗜'
  if (['md', 'txt', 'rtf'].includes(ext)) return '📝'
  if (['js', 'jsx', 'ts', 'go', 'py', 'rs', 'c', 'h', 'json', 'html', 'css', 'sh'].includes(ext)) return '⌨'
  return '📄'
}

// ── small primitives ─────────────────────────────────────────────────────────
function Btn({ children, onClick, primary, disabled, title, small }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      title={title}
      style={{
        padding: small ? '5px 10px' : '7px 13px',
        fontSize: 13,
        borderRadius: 8,
        border: `1px solid ${primary ? 'transparent' : T.borderStrong}`,
        background: primary ? T.accent : T.elevated,
        color: primary ? '#fff' : T.text,
        cursor: disabled ? 'not-allowed' : 'pointer',
        opacity: disabled ? 0.5 : 1,
        whiteSpace: 'nowrap',
        transition: 'filter .15s',
      }}
      onMouseEnter={(e) => { if (!disabled) e.currentTarget.style.filter = 'brightness(1.15)' }}
      onMouseLeave={(e) => { e.currentTarget.style.filter = 'none' }}
    >
      {children}
    </button>
  )
}

function Modal({ title, onClose, children, width = 460 }) {
  useEffect(() => {
    const h = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [onClose])
  return (
    <div
      onClick={onClose}
      style={{
        position: 'absolute', inset: 0, background: 'rgba(0,0,0,0.55)',
        display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 50, padding: 16,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          width: '100%', maxWidth: width, maxHeight: '85%', overflow: 'auto',
          background: T.surface, border: `1px solid ${T.borderStrong}`, borderRadius: 14,
          boxShadow: '0 20px 60px rgba(0,0,0,0.5)', padding: 20,
        }}
      >
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
          <h2 style={{ fontSize: 16, fontWeight: 600, color: T.text, margin: 0 }}>{title}</h2>
          <button onClick={onClose} style={{ background: 'none', border: 'none', color: T.textDim, fontSize: 20, cursor: 'pointer', lineHeight: 1 }}>×</button>
        </div>
        {children}
      </div>
    </div>
  )
}

const inputStyle = {
  width: '100%', padding: '8px 11px', fontSize: 13, borderRadius: 8,
  border: `1px solid ${T.borderStrong}`, background: T.bg, color: T.text, outline: 'none',
  boxSizing: 'border-box',
}

// ── prompt modal (name input for folder / rename) ────────────────────────────
function PromptModal({ title, label, initial, confirmText, onConfirm, onClose }) {
  const [val, setVal] = useState(initial || '')
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus(); ref.current?.select() }, [])
  const submit = () => { const v = val.trim(); if (v) onConfirm(v) }
  return (
    <Modal title={title} onClose={onClose} width={420}>
      <label style={{ fontSize: 12, color: T.textDim, display: 'block', marginBottom: 6 }}>{label}</label>
      <input
        ref={ref} style={inputStyle} value={val}
        onChange={(e) => setVal(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') submit() }}
      />
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
        <Btn onClick={onClose}>Cancel</Btn>
        <Btn primary onClick={submit} disabled={!val.trim()}>{confirmText || 'OK'}</Btn>
      </div>
    </Modal>
  )
}

// ── move (folder picker) modal ───────────────────────────────────────────────
function MoveModal({ node, onMoved, onClose }) {
  const [stack, setStack] = useState([{ id: '', name: 'My Drive' }])
  const [folders, setFolders] = useState([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState(null)
  const cur = stack[stack.length - 1]

  const load = useCallback(async (parent) => {
    setLoading(true); setErr(null)
    try {
      const r = await filesApi.list(parent)
      setFolders((r.nodes || []).filter((n) => n.is_dir && n.id !== node.id))
    } catch (e) { setErr(e.message || 'Failed to load') } finally { setLoading(false) }
  }, [node.id])

  useEffect(() => { load(cur.id) }, [cur.id, load])

  const doMove = async () => {
    try { await filesApi.move(node.id, cur.id || '', ''); onMoved() }
    catch (e) { setErr(e.message || 'Move failed') }
  }

  return (
    <Modal title={`Move “${node.name}”`} onClose={onClose}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, fontSize: 12, color: T.textDim, marginBottom: 10 }}>
        {stack.map((s, i) => (
          <span key={s.id || 'root'}>
            <button
              onClick={() => setStack(stack.slice(0, i + 1))}
              style={{ background: 'none', border: 'none', color: i === stack.length - 1 ? T.text : T.accent, cursor: 'pointer', fontSize: 12, padding: 0 }}
            >{s.name}</button>
            {i < stack.length - 1 && <span style={{ margin: '0 4px' }}>/</span>}
          </span>
        ))}
      </div>
      <div style={{ border: `1px solid ${T.border}`, borderRadius: 10, minHeight: 140, maxHeight: 240, overflow: 'auto' }}>
        {loading ? (
          <div style={{ padding: 24, textAlign: 'center', color: T.textFaint, fontSize: 13 }}>Loading…</div>
        ) : err ? (
          <div style={{ padding: 24, textAlign: 'center', color: '#f87171', fontSize: 13 }}>{err}</div>
        ) : folders.length === 0 ? (
          <div style={{ padding: 24, textAlign: 'center', color: T.textFaint, fontSize: 13 }}>No sub-folders</div>
        ) : folders.map((f) => (
          <div
            key={f.id}
            onClick={() => setStack([...stack, { id: f.id, name: f.name }])}
            style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '9px 12px', cursor: 'pointer', fontSize: 13, color: T.text, borderBottom: `1px solid ${T.border}` }}
            onMouseEnter={(e) => { e.currentTarget.style.background = T.hover }}
            onMouseLeave={(e) => { e.currentTarget.style.background = 'none' }}
          >
            <span>📁</span><span style={{ flex: 1 }}>{f.name}</span><span style={{ color: T.textFaint }}>›</span>
          </div>
        ))}
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 18 }}>
        <Btn onClick={onClose}>Cancel</Btn>
        <Btn primary onClick={doMove}>Move here</Btn>
      </div>
    </Modal>
  )
}

// ── versions modal ───────────────────────────────────────────────────────────
function VersionsModal({ node, onClose }) {
  const [versions, setVersions] = useState(null)
  const [err, setErr] = useState(null)
  useEffect(() => {
    filesApi.versions(node.id).then((r) => setVersions(r.versions || [])).catch((e) => setErr(e.message || 'Failed'))
  }, [node.id])
  return (
    <Modal title={`Versions — ${node.name}`} onClose={onClose}>
      {err ? <div style={{ color: '#f87171', fontSize: 13 }}>{err}</div>
        : versions === null ? <div style={{ color: T.textFaint, fontSize: 13 }}>Loading…</div>
        : versions.length === 0 ? <div style={{ color: T.textFaint, fontSize: 13 }}>No versions recorded yet.</div>
        : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {versions.map((v, i) => (
              <div key={v.id} style={{ display: 'flex', justifyContent: 'space-between', padding: '10px 12px', background: T.elevated, borderRadius: 8, fontSize: 12 }}>
                <div>
                  <div style={{ color: T.text, fontWeight: 600 }}>{i === 0 ? 'Current' : `Version ${versions.length - i}`}</div>
                  <div style={{ color: T.textFaint, marginTop: 2 }}>{fmtDate(v.created_at)} · {fmtSize(v.size)}</div>
                </div>
                {v.etag && <code style={{ color: T.textDim, fontSize: 11, alignSelf: 'center' }}>{String(v.etag).slice(0, 12)}</code>}
              </div>
            ))}
          </div>
        )}
    </Modal>
  )
}

// ── share modal (user grants + expiring links) ───────────────────────────────
const LINK_TTLS = [
  { label: '1 hour', s: 3600 },
  { label: '1 day', s: 86400 },
  { label: '7 days', s: 604800 },
  { label: '30 days', s: 2592000 },
]
function ShareModal({ node, onClose }) {
  const [shares, setShares] = useState([])
  const [links, setLinks] = useState([])
  const [err, setErr] = useState(null)
  const [busy, setBusy] = useState(false)
  const [principal, setPrincipal] = useState('')
  const [role, setRole] = useState('viewer')
  const [linkRole, setLinkRole] = useState('viewer')
  const [linkTtl, setLinkTtl] = useState(604800)
  const [copied, setCopied] = useState('')

  const reload = useCallback(async () => {
    try {
      const [s, l] = await Promise.all([filesApi.shares(node.id), filesApi.links(node.id)])
      setShares(s.shares || []); setLinks(l.links || []); setErr(null)
    } catch (e) { setErr(e.message || 'Failed to load sharing') }
  }, [node.id])
  useEffect(() => { reload() }, [reload])

  const addShare = async () => {
    const p = principal.trim()
    if (!p) return
    setBusy(true)
    try { await filesApi.share(node.id, p, role); setPrincipal(''); await reload() }
    catch (e) { setErr(e.message || 'Share failed') } finally { setBusy(false) }
  }
  const removeShare = async (p) => {
    setBusy(true)
    try { await filesApi.unshare(node.id, p); await reload() }
    catch (e) { setErr(e.message || 'Revoke failed') } finally { setBusy(false) }
  }
  const addLink = async () => {
    setBusy(true)
    try { await filesApi.createLink(node.id, linkRole, linkTtl); await reload() }
    catch (e) { setErr(e.message || 'Link create failed') } finally { setBusy(false) }
  }
  const killLink = async (token) => {
    setBusy(true)
    try { await filesApi.revokeLink(token); await reload() }
    catch (e) { setErr(e.message || 'Revoke failed') } finally { setBusy(false) }
  }
  const copyLink = async (token) => {
    const url = `${location.origin}/?share=${token}`
    try { await navigator.clipboard.writeText(url); setCopied(token); setTimeout(() => setCopied(''), 1500) } catch { /* ignore */ }
  }

  const section = { fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', color: T.textFaint, margin: '4px 0 10px' }

  return (
    <Modal title={`Share — ${node.name}`} onClose={onClose} width={520}>
      {err && <div style={{ color: '#f87171', fontSize: 12, marginBottom: 12 }}>{err}</div>}

      <div style={section}>Share with a person</div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
        <input
          style={{ ...inputStyle, flex: 1 }} placeholder="User ID" value={principal}
          onChange={(e) => setPrincipal(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') addShare() }}
        />
        <select value={role} onChange={(e) => setRole(e.target.value)} style={{ ...inputStyle, width: 110 }}>
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
        </select>
        <Btn primary onClick={addShare} disabled={busy || !principal.trim()}>Add</Btn>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 20 }}>
        {shares.length === 0 ? <div style={{ color: T.textFaint, fontSize: 12 }}>Not shared with anyone.</div>
          : shares.map((s) => (
            <div key={s.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 11px', background: T.elevated, borderRadius: 8, fontSize: 12 }}>
              <span style={{ flex: 1, color: T.text, overflow: 'hidden', textOverflow: 'ellipsis' }}>{s.principal_id}</span>
              <span style={{ color: T.textDim }}>{s.role}</span>
              <button onClick={() => removeShare(s.principal_id)} style={{ background: 'none', border: 'none', color: '#f87171', cursor: 'pointer', fontSize: 12 }}>Remove</button>
            </div>
          ))}
      </div>

      <div style={section}>Share links</div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
        <select value={linkRole} onChange={(e) => setLinkRole(e.target.value)} style={{ ...inputStyle, flex: 1 }}>
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
        </select>
        <select value={linkTtl} onChange={(e) => setLinkTtl(Number(e.target.value))} style={{ ...inputStyle, flex: 1 }}>
          {LINK_TTLS.map((t) => <option key={t.s} value={t.s}>{t.label}</option>)}
        </select>
        <Btn primary onClick={addLink} disabled={busy}>Create</Btn>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {links.length === 0 ? <div style={{ color: T.textFaint, fontSize: 12 }}>No share links.</div>
          : links.map((l) => {
            const expired = l.revoked || new Date(l.expires_at) < new Date()
            return (
              <div key={l.token} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 11px', background: T.elevated, borderRadius: 8, fontSize: 12, opacity: expired ? 0.5 : 1 }}>
                <span style={{ color: T.textDim }}>{l.role}</span>
                <span style={{ flex: 1, color: T.textFaint }}>
                  {l.revoked ? 'revoked' : expired ? 'expired' : `expires ${fmtDate(l.expires_at)}`}
                </span>
                {!expired && (
                  <button onClick={() => copyLink(l.token)} style={{ background: 'none', border: 'none', color: T.accent, cursor: 'pointer', fontSize: 12 }}>
                    {copied === l.token ? 'Copied!' : 'Copy'}
                  </button>
                )}
                {!l.revoked && (
                  <button onClick={() => killLink(l.token)} style={{ background: 'none', border: 'none', color: '#f87171', cursor: 'pointer', fontSize: 12 }}>Revoke</button>
                )}
              </div>
            )
          })}
      </div>
    </Modal>
  )
}

// ── row action menu ──────────────────────────────────────────────────────────
function RowMenu({ node, onAction, onClose }) {
  useEffect(() => {
    const h = () => onClose()
    window.addEventListener('click', h)
    return () => window.removeEventListener('click', h)
  }, [onClose])
  const items = [
    !node.is_dir && ['download', 'Download'],
    ['rename', 'Rename'],
    ['move', 'Move…'],
    ['share', 'Share…'],
    !node.is_dir && ['versions', 'Versions'],
    ['delete', 'Delete'],
  ].filter(Boolean)
  return (
    <div
      onClick={(e) => e.stopPropagation()}
      style={{
        position: 'absolute', right: 8, top: 30, zIndex: 30, minWidth: 150,
        background: T.surface, border: `1px solid ${T.borderStrong}`, borderRadius: 10,
        boxShadow: '0 10px 30px rgba(0,0,0,0.4)', padding: 5,
      }}
    >
      {items.map(([k, label]) => (
        <button
          key={k}
          onClick={() => { onAction(k); onClose() }}
          style={{
            display: 'block', width: '100%', textAlign: 'left', padding: '8px 11px',
            background: 'none', border: 'none', borderRadius: 7, cursor: 'pointer',
            fontSize: 13, color: k === 'delete' ? '#f87171' : T.text,
          }}
          onMouseEnter={(e) => { e.currentTarget.style.background = T.hover }}
          onMouseLeave={(e) => { e.currentTarget.style.background = 'none' }}
        >{label}</button>
      ))}
    </div>
  )
}

// ── main app ─────────────────────────────────────────────────────────────────
export default function Drive() {
  const [view, setView] = useState('mydrive') // 'mydrive' | 'shared'
  const [trail, setTrail] = useState([{ id: '', name: 'My Drive' }])
  const [nodes, setNodes] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [menuFor, setMenuFor] = useState(null)
  const [modal, setModal] = useState(null) // { kind, node }
  const [busy, setBusy] = useState(null) // status text
  const fileInputRef = useRef(null)
  const dropRef = useRef(null)
  const [dragOver, setDragOver] = useState(false)

  const cur = trail[trail.length - 1]
  const atRoot = view === 'shared' || trail.length === 1

  const refresh = useCallback(async () => {
    setLoading(true); setError(null)
    try {
      const r = view === 'shared' && trail.length === 1
        ? await filesApi.sharedWithMe()
        : await filesApi.list(cur.id)
      setNodes(r.nodes || [])
    } catch (e) {
      setError(e.message || 'Failed to load')
      setNodes([])
    } finally {
      setLoading(false)
    }
  }, [view, cur.id, trail.length])

  useEffect(() => { refresh() }, [refresh])

  const openFolder = (node) => setTrail([...trail, { id: node.id, name: node.name }])
  const gotoCrumb = (i) => setTrail(trail.slice(0, i + 1))
  const switchView = (v) => { setView(v); setTrail([{ id: '', name: v === 'shared' ? 'Shared with me' : 'My Drive' }]) }

  const onRowOpen = (node) => {
    if (node.is_dir) openFolder(node)
    else handle('download', node)
  }

  const handle = async (kind, node) => {
    if (kind === 'download') {
      setBusy(`Downloading ${node.name}…`)
      try { await downloadOne(node) } catch (e) { setError(e.message || 'Download failed') } finally { setBusy(null) }
      return
    }
    if (kind === 'delete') {
      if (!window.confirm(`Delete “${node.name}”${node.is_dir ? ' and its contents' : ''}?`)) return
      setBusy('Deleting…')
      try { await filesApi.remove(node.id); await refresh() } catch (e) { setError(e.message || 'Delete failed') } finally { setBusy(null) }
      return
    }
    setModal({ kind, node })
  }

  const doUpload = async (fileList) => {
    const files = Array.from(fileList || [])
    if (files.length === 0) return
    const parent = view === 'shared' ? '' : cur.id
    let done = 0
    for (const f of files) {
      setBusy(`Uploading ${f.name} (${++done}/${files.length})…`)
      try { await uploadOne(parent, f) } catch (e) { setError(`Upload of ${f.name} failed: ${e.message || e}`) }
    }
    setBusy(null)
    await refresh()
  }

  const onDrop = (e) => {
    e.preventDefault(); setDragOver(false)
    if (view === 'shared' && trail.length === 1) return
    if (e.dataTransfer?.files?.length) doUpload(e.dataTransfer.files)
  }

  const closeModal = async (reload) => { setModal(null); if (reload) await refresh() }

  // ── render ──────────────────────────────────────────────────────────────
  return (
    <div style={{ display: 'flex', height: '100%', background: T.bg, color: T.text, fontSize: 14, position: 'relative' }}>
      {/* sidebar */}
      <div style={{ width: 180, flexShrink: 0, borderRight: `1px solid ${T.border}`, padding: 12, display: 'flex', flexDirection: 'column', gap: 4 }}>
        <div style={{ fontSize: 15, fontWeight: 700, padding: '4px 10px 12px', color: T.text }}>Drive</div>
        {[['mydrive', '📁', 'My Drive'], ['shared', '👥', 'Shared with me']].map(([v, icon, label]) => (
          <button
            key={v}
            onClick={() => switchView(v)}
            style={{
              display: 'flex', alignItems: 'center', gap: 9, padding: '9px 11px', borderRadius: 9,
              border: 'none', cursor: 'pointer', fontSize: 13, textAlign: 'left',
              background: view === v ? T.selected : 'none',
              color: view === v ? T.text : T.textDim,
            }}
          >
            <span>{icon}</span><span>{label}</span>
          </button>
        ))}
      </div>

      {/* main */}
      <div
        ref={dropRef}
        onDragOver={(e) => { e.preventDefault(); if (!(view === 'shared' && trail.length === 1)) setDragOver(true) }}
        onDragLeave={(e) => { if (e.currentTarget === e.target) setDragOver(false) }}
        onDrop={onDrop}
        style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, position: 'relative' }}
      >
        {/* toolbar */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px', borderBottom: `1px solid ${T.border}` }}>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 3, minWidth: 0 }}>
            {trail.map((c, i) => (
              <span key={c.id || 'root'} style={{ display: 'flex', alignItems: 'center', gap: 3 }}>
                <button
                  onClick={() => gotoCrumb(i)}
                  style={{ background: 'none', border: 'none', cursor: 'pointer', fontSize: 14, fontWeight: i === trail.length - 1 ? 600 : 400, color: i === trail.length - 1 ? T.text : T.accent, padding: 0 }}
                >{c.name}</button>
                {i < trail.length - 1 && <span style={{ color: T.textFaint }}>/</span>}
              </span>
            ))}
          </div>
          {view === 'mydrive' && (
            <>
              <Btn small onClick={() => setModal({ kind: 'newfolder' })}>+ Folder</Btn>
              <Btn small primary onClick={() => fileInputRef.current?.click()}>↑ Upload</Btn>
              <input ref={fileInputRef} type="file" multiple style={{ display: 'none' }} onChange={(e) => { doUpload(e.target.files); e.target.value = '' }} />
            </>
          )}
        </div>

        {/* status / error bars */}
        {busy && <div style={{ padding: '7px 16px', fontSize: 12, color: T.accent, background: T.elevated, borderBottom: `1px solid ${T.border}` }}>{busy}</div>}
        {error && (
          <div style={{ padding: '7px 16px', fontSize: 12, color: '#f87171', background: 'rgba(248,113,113,0.08)', borderBottom: `1px solid ${T.border}`, display: 'flex', justifyContent: 'space-between' }}>
            <span>{error}</span>
            <button onClick={() => setError(null)} style={{ background: 'none', border: 'none', color: '#f87171', cursor: 'pointer' }}>×</button>
          </div>
        )}

        {/* listing */}
        <div style={{ flex: 1, overflow: 'auto' }}>
          {loading ? (
            <Center>
              <div style={{ width: 26, height: 26, border: `3px solid ${T.border}`, borderTopColor: T.accent, borderRadius: '50%', animation: 'drive-spin 0.8s linear infinite' }} />
              <span style={{ color: T.textFaint, fontSize: 13 }}>Loading…</span>
            </Center>
          ) : nodes.length === 0 ? (
            <Center>
              <div style={{ fontSize: 40, opacity: 0.4 }}>{view === 'shared' ? '👥' : '📂'}</div>
              <div style={{ color: T.textDim, fontSize: 14 }}>
                {view === 'shared' ? 'Nothing shared with you yet' : atRoot ? 'Your Drive is empty' : 'This folder is empty'}
              </div>
              {view === 'mydrive' && <Btn small primary onClick={() => fileInputRef.current?.click()}>Upload a file</Btn>}
            </Center>
          ) : (
            <div style={{ padding: 8 }}>
              {/* header row */}
              <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '6px 12px', fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.05em', color: T.textFaint }}>
                <span style={{ flex: 1 }}>Name</span>
                <span style={{ width: 90, textAlign: 'right' }}>Size</span>
                <span style={{ width: 110, textAlign: 'right' }}>Modified</span>
                <span style={{ width: 28 }} />
              </div>
              {nodes.map((node) => (
                <div
                  key={node.id}
                  onClick={() => onRowOpen(node)}
                  style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '9px 12px', borderRadius: 9, cursor: 'pointer', position: 'relative' }}
                  onMouseEnter={(e) => { e.currentTarget.style.background = T.hover }}
                  onMouseLeave={(e) => { e.currentTarget.style.background = 'none' }}
                >
                  <span style={{ fontSize: 18, width: 22, textAlign: 'center', flexShrink: 0 }}>{fileGlyph(node)}</span>
                  <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: T.text }}>{node.name}</span>
                  <span style={{ width: 90, textAlign: 'right', color: T.textFaint, fontSize: 12 }}>{node.is_dir ? '—' : fmtSize(node.size)}</span>
                  <span style={{ width: 110, textAlign: 'right', color: T.textFaint, fontSize: 12 }}>{fmtDate(node.updated_at)}</span>
                  <span style={{ width: 28, textAlign: 'center' }}>
                    <button
                      onClick={(e) => { e.stopPropagation(); setMenuFor(menuFor === node.id ? null : node.id) }}
                      style={{ background: 'none', border: 'none', color: T.textDim, cursor: 'pointer', fontSize: 18, lineHeight: 1, padding: '0 4px' }}
                    >⋯</button>
                  </span>
                  {menuFor === node.id && (
                    <RowMenu node={node} onAction={(k) => handle(k, node)} onClose={() => setMenuFor(null)} />
                  )}
                </div>
              ))}
            </div>
          )}
        </div>

        {/* drop overlay */}
        {dragOver && (
          <div style={{
            position: 'absolute', inset: 0, background: 'rgba(59,130,246,0.12)',
            border: `2px dashed ${T.accent}`, borderRadius: 12, display: 'flex',
            alignItems: 'center', justifyContent: 'center', pointerEvents: 'none', zIndex: 20,
          }}>
            <div style={{ fontSize: 16, color: T.accent, fontWeight: 600 }}>Drop files to upload</div>
          </div>
        )}
      </div>

      {/* modals */}
      {modal?.kind === 'newfolder' && (
        <PromptModal
          title="New folder" label="Folder name" confirmText="Create"
          onClose={() => setModal(null)}
          onConfirm={async (name) => {
            try { await filesApi.createFolder(cur.id, name); await closeModal(true) }
            catch (e) { setError(e.message || 'Create failed'); setModal(null) }
          }}
        />
      )}
      {modal?.kind === 'rename' && (
        <PromptModal
          title="Rename" label="New name" initial={modal.node.name} confirmText="Rename"
          onClose={() => setModal(null)}
          onConfirm={async (name) => {
            try { await filesApi.move(modal.node.id, '', name); await closeModal(true) }
            catch (e) { setError(e.message || 'Rename failed'); setModal(null) }
          }}
        />
      )}
      {modal?.kind === 'move' && (
        <MoveModal node={modal.node} onClose={() => setModal(null)} onMoved={() => closeModal(true)} />
      )}
      {modal?.kind === 'share' && <ShareModal node={modal.node} onClose={() => setModal(null)} />}
      {modal?.kind === 'versions' && <VersionsModal node={modal.node} onClose={() => setModal(null)} />}

      <style>{`@keyframes drive-spin { to { transform: rotate(360deg) } }`}</style>
    </div>
  )
}

function Center({ children }) {
  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 14, padding: 24 }}>
      {children}
    </div>
  )
}
