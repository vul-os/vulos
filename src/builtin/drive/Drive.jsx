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
  // Account-only sharing: resolve a recipient EMAIL (directory) and route by
  // locality — co-cloud → ACL grant; remote → a delivered per-document capability.
  shareByEmail: (node_id, email, role, ttl_seconds) =>
    request('/files/share-by-email', { method: 'POST', body: JSON.stringify({ node_id, email, role, ttl_seconds }) }),
  unshare: (node_id, principal_id) =>
    request('/files/unshare', { method: 'POST', body: JSON.stringify({ node_id, principal_id }) }),
  links: (node) => request(`/files/share-links?node=${encodeURIComponent(node)}`),
  createLink: (node_id, role, ttl_seconds) =>
    request('/files/share-link', { method: 'POST', body: JSON.stringify({ node_id, role, ttl_seconds }) }),
  revokeLink: (token) =>
    request('/files/share-link/revoke', { method: 'POST', body: JSON.stringify({ token }) }),

  // ── OS peer-share (Mechanism B, bucket-less box-to-box) ──
  peerIssue: (node_id, access, recipient, ttl_seconds) =>
    request('/files/peer/issue', { method: 'POST', body: JSON.stringify({ node_id, access, recipient, ttl_seconds }) }),
  peerShares: (node) => request(`/files/peer/shares?node=${encodeURIComponent(node)}`),
  peerRevoke: (id) => request('/files/peer/revoke', { method: 'POST', body: JSON.stringify({ id }) }),
  peerRedeem: (link) => request('/files/peer/redeem', { method: 'POST', body: JSON.stringify({ link }) }),
  peerReceived: () => request('/files/peer/received'),
  peerSave: (id, parent_id, name) =>
    request('/files/peer/save', { method: 'POST', body: JSON.stringify({ id, parent_id, name }) }),

  // ── external stores (Phase 4: Google Drive / Dropbox / GCS as virtual drives) ──
  externalStatus: () => request('/files/external/status'),
  externalMounts: () => request('/files/external/mounts'),
  externalConnect: (provider, name, config) =>
    request('/files/external/connect', { method: 'POST', body: JSON.stringify({ provider, name, config }) }),
  externalDisconnect: (id) =>
    request('/files/external/disconnect', { method: 'POST', body: JSON.stringify({ id }) }),
  externalList: (mount, folder) =>
    request(`/files/external/list?mount=${encodeURIComponent(mount)}&folder=${encodeURIComponent(folder || '')}`),
  externalFolder: (mount, parent, name) =>
    request('/files/external/folder', { method: 'POST', body: JSON.stringify({ mount, parent, name }) }),

  // ── import (copy provider files INTO your owned Drive — distinct from mount) ──
  // A mount is a live view that vanishes with the provider; an import is a copy
  // you keep even after disconnecting the integration or deleting the upstream.
  importStatus: () => request('/files/import/status'),
  importJobs: () => request('/files/import/jobs'),
  importStart: (provider, source, mode) =>
    request('/files/import', { method: 'POST', body: JSON.stringify({ provider, source, mode }) }),
  importSync: (id) => request(`/files/import/jobs/${encodeURIComponent(id)}/sync`, { method: 'POST' }),
  importDelete: (id) => request(`/files/import/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' }),
}

// uploadExternalOne: stream a file's bytes into a writable external mount under
// parent, then return the created node. conflict defaults to rename-on-collision.
async function uploadExternalOne(mountId, parentId, file, conflict = 'rename') {
  const qs = new URLSearchParams({ mount: mountId, parent: parentId || '', name: file.name, conflict })
  const res = await rawFetch(`/files/external/upload?${qs.toString()}`, {
    method: 'POST',
    body: file,
    headers: file.type ? { 'Content-Type': file.type } : {},
  })
  if (!res.ok) {
    if (res.status === 409) throw new Error('A file with that name already exists')
    throw new Error(`upload failed (${res.status})`)
  }
  return res.json()
}

// downloadExternal: stream a mounted-store file's bytes through the OS and save.
async function downloadExternal(mountId, node) {
  const res = await rawFetch(
    `/files/external/content?mount=${encodeURIComponent(mountId)}&file=${encodeURIComponent(node.id)}&name=${encodeURIComponent(node.name)}`,
  )
  if (!res.ok) throw new Error(`download failed (${res.status})`)
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = node.name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
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

// downloadNodeBytes fetches a node's raw content bytes (presigned or OS data plane).
async function downloadNodeBytes(node) {
  const { grant } = await filesApi.downloadGrant(node.id)
  let res
  if (grant && grant.type === 'presigned' && grant.url) {
    res = await fetch(grant.url)
  } else {
    res = await rawFetch(`/files/content?node=${encodeURIComponent(node.id)}`)
  }
  if (!res.ok) throw new Error(`download failed (${res.status})`)
  return new Uint8Array(await res.arrayBuffer())
}

// downloadFolderTar fetches a folder subtree as a tar archive (owner-authed) so it
// can be sealed for a content-blind share. Mirrors the server /peer/folder-tar route.
async function downloadFolderTar(node) {
  const res = await rawFetch(`/files/peer/folder-tar?node=${encodeURIComponent(node.id)}`)
  if (!res.ok) throw new Error(`could not read folder (${res.status})`)
  return new Uint8Array(await res.arrayBuffer())
}

// maybeDecrypt transparently decrypts WAVE-3 content-blind (VSEAL1) bytes with the
// in-memory master key and recovers the sealed WAVE-7 metadata. Returns
// { bytes, meta } where meta = { name, content_type, is_dir } | null. Non-sealed
// bytes pass through with meta=null. The recipient's browser opens seals here on
// download; the cell only ever handled the ciphertext + opaque metadata.
async function maybeDecrypt(bytes) {
  const { isSealed, open, unpackMeta } = await import('../../lib/contentSeal.js')
  if (!isSealed(bytes)) return { bytes, meta: null }
  const { getMasterKey } = await import('../../lib/masterKey.js')
  const mk = getMasterKey()
  if (!mk) throw new Error('This file is encrypted; unlock your account to open it.')
  const pt = await open(bytes, mk)
  return unpackMeta(pt)
}

// extractSealedFolderToDrive untars a decrypted folder payload into the recipient's
// Drive under parentId, recreating the subtree with filesApi. Returns the count of
// created files. The cell never saw this tar — it is reconstructed client-side.
async function extractSealedFolderToDrive(rootName, tarBytes, parentId) {
  const { untar } = await import('../../lib/contentSeal.js')
  const entries = untar(tarBytes)
  const root = await filesApi.createFolder(parentId || '', rootName)
  const dirIds = { '': root.id } // relative-path → folder node id
  const ensureDir = async (relPath) => {
    if (relPath in dirIds) return dirIds[relPath]
    const slash = relPath.lastIndexOf('/')
    const parentRel = slash < 0 ? '' : relPath.slice(0, slash)
    const name = slash < 0 ? relPath : relPath.slice(slash + 1)
    const pid = await ensureDir(parentRel)
    const f = await filesApi.createFolder(pid, name)
    dirIds[relPath] = f.id
    return f.id
  }
  let files = 0
  for (const e of entries) {
    // Strip the archive's own root component (streamFolderTar roots at the folder).
    const rel = e.name.split('/').slice(1).join('/')
    if (!rel) continue
    if (e.isDir) { await ensureDir(rel); continue }
    const slash = rel.lastIndexOf('/')
    const dirRel = slash < 0 ? '' : rel.slice(0, slash)
    const fname = slash < 0 ? rel : rel.slice(slash + 1)
    const pid = await ensureDir(dirRel)
    await uploadOne(pid, new File([e.bytes], fname))
    files++
  }
  return files
}

function saveBlob(bytes, name) {
  const url = URL.createObjectURL(new Blob([bytes]))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// downloadOne: fetch bytes → (content-blind) decrypt if sealed → recover metadata →
// save the file under its REAL name, or (a sealed folder) untar it into Drive under
// destParentId. Returns { extractedFolder, files } when a folder was reconstructed.
async function downloadOne(node, destParentId) {
  const { bytes, meta } = await maybeDecrypt(await downloadNodeBytes(node))
  if (meta && meta.is_dir) {
    const files = await extractSealedFolderToDrive(meta.name || node.name, bytes, destParentId || '')
    return { extractedFolder: meta.name || node.name, files }
  }
  saveBlob(bytes, (meta && meta.name) || node.name)
  return {}
}

// shareFileContentBlind performs a CONTENT-BLIND remote share: it resolves the
// recipient's PUBLISHED X25519 content key from the directory, seals the file bytes
// to it (and to the sharer's own key) client-side, and hands the CIPHERTEXT to
// /files/peer/issue-sealed. The relaying cell only ever sees the sealed envelope.
// FAIL CLOSED: a directory-resolvable recipient with NO published content key is
// refused (never a plaintext fallback through the cell). Returns { ok, blind, note }.
async function shareFileContentBlind(node, email, access, ttlSeconds) {
  let disc
  try {
    disc = await request(`/peering/discover?email=${encodeURIComponent(email)}`)
  } catch {
    return { ok: false, blind: false } // not directory-resolvable → caller falls back
  }
  const rec = disc && disc.found ? disc.result : null
  if (!rec) return { ok: false, blind: false } // co-cloud/local or unknown → caller falls back
  if (!rec.content_pub_key) {
    // Remote recipient that has published NO content key: cannot share content-blind.
    throw new Error(`${email} has not published an encryption key yet — cannot share securely.`)
  }
  const { getMasterKey } = await import('../../lib/masterKey.js')
  const mk = getMasterKey()
  if (!mk) throw new Error('Unlock your account to share encrypted files.')
  const { deriveContentPubKeyB64, seal, packMeta } = await import('../../lib/contentSeal.js')
  const myPub = await deriveContentPubKeyB64(mk)
  // WAVE-7: a folder is sealed as its TAR archive (the recipient untars after
  // decrypt); a file is sealed as its bytes. Name/type/is_dir ride INSIDE the seal
  // (packMeta) so the relaying cell sees no filename — only opaque ciphertext.
  const payload = node.is_dir ? await downloadFolderTar(node) : await downloadNodeBytes(node)
  const packed = packMeta(
    { name: node.name, content_type: node.content_type || '', is_dir: !!node.is_dir },
    payload,
  )
  const sealed = await seal(packed, [rec.content_pub_key, myPub])
  const fd = new FormData()
  fd.append('node_id', node.id)
  fd.append('email', email)
  fd.append('access', access)
  if (ttlSeconds) fd.append('ttl_seconds', String(ttlSeconds))
  fd.append('sealed', new Blob([sealed], { type: 'application/octet-stream' }), 'sealed.vseal')
  const res = await rawFetch('/files/peer/issue-sealed', { method: 'POST', body: fd })
  if (!res.ok) throw new Error(`Secure share failed (${res.status})`)
  const out = await res.json()
  return { ok: true, blind: true, result: out }
}

// downloadReceived: fetch a redeemed item's staged bytes, (content-blind) decrypt
// if sealed, and trigger a save.
async function downloadReceived(item) {
  const res = await rawFetch(`/files/peer/received/get?id=${encodeURIComponent(item.id)}`)
  if (!res.ok) throw new Error(`download failed (${res.status})`)
  const { bytes, meta } = await maybeDecrypt(new Uint8Array(await res.arrayBuffer()))
  if (meta && meta.is_dir) {
    const files = await extractSealedFolderToDrive(meta.name || item.name, bytes, '')
    return { extractedFolder: meta.name || item.name, files }
  }
  saveBlob(bytes, (meta && meta.name) || (item.is_dir ? `${item.name}.tar` : item.name))
  return {}
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
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('viewer')
  const [note, setNote] = useState(null)
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
    const addr = email.trim()
    if (!addr) return
    setBusy(true); setNote(null)
    try {
      // WAVE-3/7: prefer a CONTENT-BLIND share for directory-resolvable (remote)
      // recipients — the file (or, for a folder, its tar) is sealed to the recipient's
      // published key so the relaying cell only sees ciphertext. Both files AND
      // folders are sealed (WAVE-7); co-cloud/local recipients use the ordinary path.
      {
        const blind = await shareFileContentBlind(node, addr, role, 0)
        if (blind.blind) {
          setEmail('')
          const r = blind.result || {}
          setNote(r.delivered
            ? `Sent to ${addr}, end-to-end encrypted (delivered to ${r.server || 'their server'}).`
            : `Encrypted for ${addr}. Could not auto-deliver — copy the link: ${r.link || ''}`)
          setErr(null)
          await reload()
          return
        }
      }
      const r = await filesApi.shareByEmail(node.id, addr, role, 0)
      setEmail('')
      // Surface the routing outcome: co-cloud grants show in the list below;
      // remote shares mint+deliver a capability to the recipient's server.
      if (r && r.mode === 'remote') {
        setNote(r.delivered
          ? `Sent to ${addr} (delivered to ${r.server || 'their server'}).`
          : `Shared with ${addr}. Could not auto-deliver — copy the link: ${r.link || ''}`)
      } else {
        setNote(`Shared with ${addr}.`)
      }
      setErr(null)
      await reload()
    }
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
          type="email" autoComplete="off"
          style={{ ...inputStyle, flex: 1 }} placeholder="Email address" value={email}
          onChange={(e) => setEmail(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') addShare() }}
        />
        <select value={role} onChange={(e) => setRole(e.target.value)} style={{ ...inputStyle, width: 110 }}>
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
        </select>
        <Btn primary onClick={addShare} disabled={busy || !email.trim()}>Add</Btn>
      </div>
      {note && <div style={{ color: T.textDim, fontSize: 12, marginBottom: 8, wordBreak: 'break-all' }}>{note}</div>}
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

// ── peer-share modal (Mechanism B: bucket-less capability over p2p) ──────────
const CAP_TTLS = [
  { label: '1 hour', s: 3600 },
  { label: '1 day', s: 86400 },
  { label: '7 days', s: 604800 },
]
function PeerShareModal({ node, onClose }) {
  const [shares, setShares] = useState([])
  const [err, setErr] = useState(null)
  const [busy, setBusy] = useState(false)
  const [access, setAccess] = useState('viewer')
  const [recipient, setRecipient] = useState('')
  const [ttl, setTtl] = useState(86400)
  const [link, setLink] = useState('')
  const [copied, setCopied] = useState(false)

  const reload = useCallback(async () => {
    try { const r = await filesApi.peerShares(node.id); setShares(r.shares || []); setErr(null) }
    catch (e) { setErr(e.message || 'Failed to load') }
  }, [node.id])
  useEffect(() => { reload() }, [reload])

  const generate = async () => {
    setBusy(true); setLink(''); setCopied(false)
    try {
      const r = await filesApi.peerIssue(node.id, access, recipient.trim(), ttl)
      setLink(r.link || '')
      await reload()
    } catch (e) { setErr(e.message || 'Generate failed') } finally { setBusy(false) }
  }
  const revoke = async (id) => {
    setBusy(true)
    try { await filesApi.peerRevoke(id); await reload() }
    catch (e) { setErr(e.message || 'Revoke failed') } finally { setBusy(false) }
  }
  const copy = async () => {
    try { await navigator.clipboard.writeText(link); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { /* ignore */ }
  }
  const section = { fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', color: T.textFaint, margin: '4px 0 10px' }

  return (
    <Modal title={`Share via peer — ${node.name}`} onClose={onClose} width={540}>
      {err && <div style={{ color: '#f87171', fontSize: 12, marginBottom: 12 }}>{err}</div>}
      <div style={{ fontSize: 12, color: T.textDim, marginBottom: 14, lineHeight: 1.5 }}>
        Generate a signed, expiring capability link. The recipient redeems it on their own
        Vulos box — bytes stream box-to-box, no bucket or cloud required. Leave the recipient
        blank for anyone-with-the-link, or bind it to a specific peer’s Vula ID.
      </div>

      <div style={section}>New capability</div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
        <select value={access} onChange={(e) => setAccess(e.target.value)} style={{ ...inputStyle, width: 120 }}>
          <option value="viewer">Viewer</option>
          <option value="editor">Editor</option>
        </select>
        <select value={ttl} onChange={(e) => setTtl(Number(e.target.value))} style={{ ...inputStyle, width: 120 }}>
          {CAP_TTLS.map((t) => <option key={t.s} value={t.s}>{t.label}</option>)}
        </select>
        <input
          style={{ ...inputStyle, flex: 1 }} placeholder="Recipient Vula ID (optional)"
          value={recipient} onChange={(e) => setRecipient(e.target.value)}
        />
      </div>
      <Btn primary onClick={generate} disabled={busy}>Generate capability link</Btn>

      {link && (
        <div style={{ marginTop: 12, padding: 12, background: T.elevated, borderRadius: 10 }}>
          <div style={{ fontSize: 11, color: T.textFaint, marginBottom: 6 }}>Share this link with the recipient:</div>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <code style={{ flex: 1, fontSize: 11, color: T.text, wordBreak: 'break-all', maxHeight: 80, overflow: 'auto' }}>{link}</code>
            <Btn small onClick={copy}>{copied ? 'Copied!' : 'Copy'}</Btn>
          </div>
        </div>
      )}

      <div style={{ ...section, marginTop: 20 }}>Active capabilities</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {shares.length === 0 ? <div style={{ color: T.textFaint, fontSize: 12 }}>No peer capabilities issued.</div>
          : shares.map((s) => {
            const expired = s.revoked || new Date(s.expires_at) < new Date()
            return (
              <div key={s.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '8px 11px', background: T.elevated, borderRadius: 8, fontSize: 12, opacity: expired ? 0.5 : 1 }}>
                <span style={{ color: T.textDim }}>{s.access}</span>
                <span style={{ flex: 1, color: T.textFaint, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {s.recipient ? `→ ${s.recipient.slice(0, 18)}…` : 'anyone with link'}
                  {' · '}
                  {s.revoked ? 'revoked' : expired ? 'expired' : `expires ${fmtDate(s.expires_at)}`}
                </span>
                {!s.revoked && (
                  <button onClick={() => revoke(s.id)} style={{ background: 'none', border: 'none', color: '#f87171', cursor: 'pointer', fontSize: 12 }}>Revoke</button>
                )}
              </div>
            )
          })}
      </div>
    </Modal>
  )
}

// ── redeem modal (paste a capability link → fetch over p2p → preview/save) ───
function RedeemModal({ onClose, onRedeemed }) {
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)
  const [item, setItem] = useState(null)

  const redeem = async () => {
    const v = link.trim()
    if (!v) return
    setBusy(true); setErr(null)
    try { const it = await filesApi.peerRedeem(v); setItem(it); onRedeemed?.() }
    catch (e) { setErr(e.message || 'Redeem failed') } finally { setBusy(false) }
  }
  const save = async () => {
    setBusy(true)
    try { await filesApi.peerSave(item.id, '', item.name); onClose() }
    catch (e) { setErr(e.message || 'Save failed') } finally { setBusy(false) }
  }

  return (
    <Modal title="Redeem a capability link" onClose={onClose} width={520}>
      {err && <div style={{ color: '#f87171', fontSize: 12, marginBottom: 12 }}>{err}</div>}
      {!item ? (
        <>
          <div style={{ fontSize: 12, color: T.textDim, marginBottom: 10, lineHeight: 1.5 }}>
            Paste a capability link someone shared with you. Vulos verifies it and streams the
            bytes directly from their box to yours.
          </div>
          <textarea
            style={{ ...inputStyle, minHeight: 90, resize: 'vertical', fontFamily: 'monospace', fontSize: 11 }}
            placeholder="Paste capability link…" value={link} onChange={(e) => setLink(e.target.value)}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
            <Btn onClick={onClose}>Cancel</Btn>
            <Btn primary onClick={redeem} disabled={busy || !link.trim()}>{busy ? 'Fetching…' : 'Redeem'}</Btn>
          </div>
        </>
      ) : (
        <>
          <div style={{ padding: 14, background: T.elevated, borderRadius: 10, marginBottom: 16 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <span style={{ fontSize: 24 }}>{item.is_dir ? '📁' : '📄'}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ color: T.text, fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis' }}>{item.name}</div>
                <div style={{ color: T.textFaint, fontSize: 12 }}>{item.is_dir ? 'folder' : fmtSize(item.size)} · received</div>
              </div>
            </div>
          </div>
          <div style={{ fontSize: 12, color: T.textDim, marginBottom: 14 }}>
            Received and staged. Save it into your own Drive to keep it.
          </div>
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <Btn onClick={onClose}>Close</Btn>
            <Btn primary onClick={save} disabled={busy}>{busy ? 'Saving…' : 'Save to Drive'}</Btn>
          </div>
        </>
      )}
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
    ['peershare', 'Share via peer…'],
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

// ── connect-external modal (mount Google Drive / Dropbox / GCS) ──────────────
function ConnectModal({ providers, onConnect, onClose }) {
  const [provider, setProvider] = useState(providers[0]?.kind || '')
  const [config, setConfig] = useState({})
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)
  const sel = providers.find((p) => p.kind === provider)
  const fields = sel?.config_fields || []

  // Reset collected config whenever the chosen provider changes.
  const pick = (kind) => { setProvider(kind); setConfig({}) }

  const missing = fields.some((f) => f.required && !(config[f.key] || '').trim())

  const connect = async () => {
    setBusy(true); setErr(null)
    try { await onConnect(provider, config); onClose() }
    catch (e) {
      // A 409 means the account hasn't linked the provider in cloud settings yet.
      const msg = /409|not connected/i.test(e.message || '')
        ? 'This account has not connected the provider yet. Connect it in your cloud account settings, then try again.'
        : (e.message || 'Connect failed')
      setErr(msg)
    } finally { setBusy(false) }
  }

  return (
    <Modal title="Connect an external drive" onClose={onClose} width={460}>
      {err && <div style={{ color: '#f87171', fontSize: 12, marginBottom: 12 }}>{err}</div>}
      <div style={{ fontSize: 12, color: T.textDim, marginBottom: 14, lineHeight: 1.5 }}>
        Mount an external store as a drive. Vulos browses it using a short-lived token
        brokered by your cloud account — the provider’s long-lived credentials never
        touch this box. {sel?.writable
          ? 'You can upload and create folders directly in this drive.'
          : 'This drive is read-only.'}
      </div>
      <label style={{ fontSize: 12, color: T.textDim, display: 'block', marginBottom: 6 }}>Provider</label>
      <select value={provider} onChange={(e) => pick(e.target.value)} style={{ ...inputStyle, marginBottom: 18 }}>
        {providers.map((p) => <option key={p.kind} value={p.kind}>{p.display_name}</option>)}
      </select>
      {fields.map((f) => (
        <div key={f.key} style={{ marginBottom: 14 }}>
          <label style={{ fontSize: 12, color: T.textDim, display: 'block', marginBottom: 6 }}>
            {f.label}{f.required ? ' *' : ''}
          </label>
          <input
            value={config[f.key] || ''}
            onChange={(e) => setConfig({ ...config, [f.key]: e.target.value })}
            placeholder={f.label}
            style={inputStyle}
          />
        </div>
      ))}
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <Btn onClick={onClose}>Cancel</Btn>
        <Btn primary onClick={connect} disabled={busy || !provider || missing}>
          {busy ? 'Connecting…' : `Connect ${sel?.display_name || ''}`}
        </Btn>
      </div>
    </Modal>
  )
}

// ── import modal (copy provider files INTO your Drive) ───────────────────────
// Distinct from ConnectModal (mount): this writes Vulos-OWNED copies that
// persist after the integration is disconnected or the upstream file is deleted.
// Lets the user pick a source, browse to a folder (or "everything"), choose
// "Import once" vs "Keep in sync", and shows job progress + history.
function ImportModal({ sources, jobs, onStart, onSync, onDelete, onClose }) {
  const [provider, setProvider] = useState(sources[0]?.kind || '')
  const [mode, setMode] = useState('once')
  // Folder picker over the provider tree (via a throwaway-style browse). For the
  // foundation we offer "Everything" plus a typed source-folder id; deep
  // provider browsing reuses the mount list endpoint when a mount exists.
  const [source, setSource] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState(null)
  const sel = sources.find((p) => p.kind === provider)

  const start = async () => {
    setBusy(true); setErr(null)
    try { await onStart(provider, source.trim(), mode) }
    catch (e) {
      const msg = /409|not connected/i.test(e.message || '')
        ? 'This account has not connected the provider yet. Connect it in your cloud account settings, then try again.'
        : (e.message || 'Import failed to start')
      setErr(msg)
    } finally { setBusy(false) }
  }

  const section = { fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em', color: T.textFaint, margin: '16px 0 8px' }
  const statusColor = (s) => s === 'done' ? '#34d399' : s === 'error' ? '#f87171' : T.accent

  return (
    <Modal title="Import from Google / Microsoft" onClose={onClose} width={560}>
      {err && <div style={{ color: '#f87171', fontSize: 12, marginBottom: 12 }}>{err}</div>}
      <div style={{ fontSize: 12, color: T.textDim, marginBottom: 6, lineHeight: 1.5 }}>
        <strong style={{ color: T.text }}>Import = a copy you own.</strong> Vulos pulls the files into your
        Drive. The copy stays even after you disconnect the integration or the
        original is deleted. (That’s different from <em>Connect a drive</em>, which
        is a live view that disappears with the provider.)
      </div>

      <div style={section}>Source</div>
      <div style={{ display: 'flex', gap: 8 }}>
        <select value={provider} onChange={(e) => setProvider(e.target.value)} style={{ ...inputStyle, flex: 1 }}>
          {sources.map((p) => <option key={p.kind} value={p.kind}>{p.display_name}</option>)}
        </select>
      </div>
      <div style={{ marginTop: 8 }}>
        <input
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="Folder id (leave blank for everything)"
          style={inputStyle}
        />
        <div style={{ fontSize: 11, color: T.textFaint, marginTop: 4 }}>
          Blank imports everything in {sel?.display_name || 'the source'}. Google
          Docs/Sheets/Slides are saved as Office files (.docx/.xlsx/.pptx).
        </div>
      </div>

      <div style={section}>How</div>
      <div style={{ display: 'flex', gap: 8 }}>
        {[['once', 'Import once', 'A single copy.'], ['sync', 'Keep in sync', 'Re-pull adds new files; never deletes your copies.']].map(([v, label, hint]) => (
          <button
            key={v}
            onClick={() => setMode(v)}
            style={{
              flex: 1, textAlign: 'left', padding: '10px 12px', borderRadius: 10, cursor: 'pointer',
              border: `1px solid ${mode === v ? T.accent : T.borderStrong}`,
              background: mode === v ? T.selected : T.elevated, color: T.text,
            }}
          >
            <div style={{ fontSize: 13, fontWeight: 600 }}>{label}</div>
            <div style={{ fontSize: 11, color: T.textFaint, marginTop: 2 }}>{hint}</div>
          </button>
        ))}
      </div>

      <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 16 }}>
        <Btn primary onClick={start} disabled={busy || !provider}>{busy ? 'Starting…' : 'Start import'}</Btn>
      </div>

      <div style={section}>Imports</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {jobs.length === 0 ? <div style={{ color: T.textFaint, fontSize: 12 }}>No imports yet.</div>
          : jobs.map((j) => (
            <div key={j.id} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '9px 11px', background: T.elevated, borderRadius: 8, fontSize: 12 }}>
              <span style={{ flex: 1, minWidth: 0 }}>
                <span style={{ color: T.text }}>{j.provider}</span>
                <span style={{ color: T.textFaint }}>{' · '}{j.mode === 'sync' ? 'sync' : 'once'}{j.source ? ` · ${String(j.source).slice(0, 10)}…` : ' · everything'}</span>
                <span style={{ display: 'block', color: T.textFaint, marginTop: 2 }}>
                  {j.imported || 0} copied · {j.skipped || 0} skipped{j.errors ? ` · ${j.errors} errors` : ''}
                </span>
              </span>
              <span style={{ color: statusColor(j.status), fontWeight: 600 }}>{j.status}</span>
              {j.mode === 'sync' && j.status !== 'running' && (
                <button onClick={() => onSync(j.id)} style={{ background: 'none', border: 'none', color: T.accent, cursor: 'pointer', fontSize: 12 }}>Sync now</button>
              )}
              <button onClick={() => onDelete(j.id)} style={{ background: 'none', border: 'none', color: '#f87171', cursor: 'pointer', fontSize: 12 }}>Remove</button>
            </div>
          ))}
      </div>
    </Modal>
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
  const [redeemOpen, setRedeemOpen] = useState(false)
  const [connectOpen, setConnectOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [extStatus, setExtStatus] = useState({ available: false, providers: [] })
  const [importStatus, setImportStatus] = useState({ available: false, sources: [] })
  const [importJobs, setImportJobs] = useState([])
  const [mounts, setMounts] = useState([])
  const [busy, setBusy] = useState(null) // status text

  // The active external mount, if the current view is one ("ext:<mountID>").
  const extMountId = view.startsWith('ext:') ? view.slice(4) : ''
  const extMount = mounts.find((m) => m.id === extMountId)
  const fileInputRef = useRef(null)
  const dropRef = useRef(null)
  const [dragOver, setDragOver] = useState(false)

  const cur = trail[trail.length - 1]
  const atRoot = view === 'shared' || trail.length === 1

  // Load external-store availability + the user's mounts once on open.
  const loadExternal = useCallback(async () => {
    try {
      const st = await filesApi.externalStatus()
      setExtStatus(st || { available: false, providers: [] })
      if (st?.available) {
        const r = await filesApi.externalMounts()
        setMounts(r.mounts || [])
      }
    } catch { /* external is optional; ignore when unavailable */ }
  }, [])
  useEffect(() => { loadExternal() }, [loadExternal])

  // Load import availability + the user's jobs. While a job is running, poll so
  // the progress/history view updates live.
  const loadImport = useCallback(async () => {
    try {
      const st = await filesApi.importStatus()
      setImportStatus(st || { available: false, sources: [] })
      if (st?.available) {
        const r = await filesApi.importJobs()
        setImportJobs(r.jobs || [])
      }
    } catch { /* import is optional; ignore when unavailable */ }
  }, [])
  useEffect(() => { loadImport() }, [loadImport])
  useEffect(() => {
    if (!importJobs.some((j) => j.status === 'running' || j.status === 'pending')) return
    const t = setInterval(loadImport, 2000)
    return () => clearInterval(t)
  }, [importJobs, loadImport])

  const refresh = useCallback(async () => {
    setLoading(true); setError(null)
    try {
      if (view.startsWith('ext:')) {
        const r = await filesApi.externalList(view.slice(4), cur.id)
        setNodes(r.nodes || [])
      } else if (view === 'received') {
        const r = await filesApi.peerReceived()
        setNodes(r.items || [])
      } else {
        const r = view === 'shared' && trail.length === 1
          ? await filesApi.sharedWithMe()
          : await filesApi.list(cur.id)
        setNodes(r.nodes || [])
      }
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
  const VIEW_NAMES = { shared: 'Shared with me', received: 'Received', mydrive: 'My Drive' }
  const switchView = (v, rootName) => { setView(v); setTrail([{ id: '', name: rootName || VIEW_NAMES[v] || 'My Drive' }]) }

  const connectExternal = async (provider, config) => {
    await filesApi.externalConnect(provider, '', config)
    await loadExternal()
  }
  const disconnectExternal = async (m) => {
    if (!window.confirm(`Disconnect “${m.name}”? Your files stay in ${m.name}; this only removes the drive from Vulos.`)) return
    try {
      await filesApi.externalDisconnect(m.id)
      if (extMountId === m.id) switchView('mydrive')
      await loadExternal()
    } catch (e) { setError(e.message || 'Disconnect failed') }
  }

  // Import handlers. After a start/sync we refresh the job list (and My Drive,
  // since copies land there) so progress + new files show up.
  const startImport = async (provider, source, mode) => {
    await filesApi.importStart(provider, source, mode)
    await loadImport()
    setTimeout(() => { loadImport(); if (view === 'mydrive') refresh() }, 1500)
  }
  const syncImport = async (id) => {
    try { await filesApi.importSync(id); await loadImport() }
    catch (e) { setError(e.message || 'Sync failed') }
  }
  const deleteImport = async (id) => {
    if (!window.confirm('Remove this import? Your imported files stay in your Drive; this only stops tracking the import.')) return
    try { await filesApi.importDelete(id); await loadImport() }
    catch (e) { setError(e.message || 'Remove failed') }
  }

  const saveReceived = async (item) => {
    setBusy(`Saving ${item.name} to Drive…`)
    try { await filesApi.peerSave(item.id, '', item.name); await refresh() }
    catch (e) { setError(e.message || 'Save failed') } finally { setBusy(null) }
  }

  const onRowOpen = (node) => {
    if (node.is_dir) { openFolder(node); return }
    if (extMountId) {
      setBusy(`Downloading ${node.name}…`)
      downloadExternal(extMountId, node).catch((e) => setError(e.message || 'Download failed')).finally(() => setBusy(null))
      return
    }
    handle('download', node)
  }

  const handle = async (kind, node) => {
    if (kind === 'download') {
      setBusy(`Downloading ${node.name}…`)
      try {
        const r = await downloadOne(node, cur.id)
        if (r && r.extractedFolder) await refresh() // a sealed folder was untarred into Drive
      } catch (e) { setError(e.message || 'Download failed') } finally { setBusy(null) }
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

  // A writable external mount is an upload/new-folder target, just like My Drive.
  const extWritable = !!extMount?.writable

  const doUpload = async (fileList) => {
    const files = Array.from(fileList || [])
    if (files.length === 0) return
    let done = 0
    for (const f of files) {
      setBusy(`Uploading ${f.name} (${++done}/${files.length})…`)
      try {
        if (extMountId) await uploadExternalOne(extMountId, cur.id, f)
        else await uploadOne(view === 'shared' ? '' : cur.id, f)
      } catch (e) { setError(`Upload of ${f.name} failed: ${e.message || e}`) }
    }
    setBusy(null)
    await refresh()
  }

  // Where uploads/new-folder are allowed: My Drive, or a writable external mount.
  const canWrite = view === 'mydrive' || extWritable
  const readOnlyView = !canWrite // shared / received / read-only external aren't targets
  const onDrop = (e) => {
    e.preventDefault(); setDragOver(false)
    if (readOnlyView) return
    if (e.dataTransfer?.files?.length) doUpload(e.dataTransfer.files)
  }

  const closeModal = async (reload) => { setModal(null); if (reload) await refresh() }

  // ── render ──────────────────────────────────────────────────────────────
  return (
    <div style={{ display: 'flex', height: '100%', background: T.bg, color: T.text, fontSize: 14, position: 'relative' }}>
      {/* sidebar */}
      <div style={{ width: 180, flexShrink: 0, borderRight: `1px solid ${T.border}`, padding: 12, display: 'flex', flexDirection: 'column', gap: 4 }}>
        <div style={{ fontSize: 15, fontWeight: 700, padding: '4px 10px 12px', color: T.text }}>Drive</div>
        {[['mydrive', '📁', 'My Drive'], ['shared', '👥', 'Shared with me'], ['received', '📥', 'Received']].map(([v, icon, label]) => (
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

        {/* external drives (Google Drive etc.) — only when the seam is wired */}
        {(extStatus.available || mounts.length > 0) && (
          <>
            <div style={{ fontSize: 10, textTransform: 'uppercase', letterSpacing: '0.06em', color: T.textFaint, padding: '14px 10px 4px' }}>External</div>
            {mounts.map((m) => {
              const v = `ext:${m.id}`
              return (
                <div key={m.id} style={{ position: 'relative', display: 'flex', alignItems: 'center' }}>
                  <button
                    onClick={() => switchView(v, m.name)}
                    title={m.name}
                    style={{
                      flex: 1, display: 'flex', alignItems: 'center', gap: 9, padding: '9px 11px', borderRadius: 9,
                      border: 'none', cursor: 'pointer', fontSize: 13, textAlign: 'left', minWidth: 0,
                      background: view === v ? T.selected : 'none', color: view === v ? T.text : T.textDim,
                    }}
                  >
                    <span>🟢</span>
                    <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{m.name}</span>
                  </button>
                  <button
                    onClick={() => disconnectExternal(m)}
                    title="Disconnect"
                    style={{ background: 'none', border: 'none', color: T.textFaint, cursor: 'pointer', fontSize: 14, padding: '0 6px' }}
                  >×</button>
                </div>
              )
            })}
            <button
              onClick={() => extStatus.available && setConnectOpen(true)}
              disabled={!extStatus.available}
              title={extStatus.available ? 'Connect an external drive' : 'External drives require a connected cloud account'}
              style={{
                display: 'flex', alignItems: 'center', gap: 9, padding: '9px 11px', borderRadius: 9,
                border: 'none', cursor: extStatus.available ? 'pointer' : 'not-allowed', fontSize: 13, textAlign: 'left',
                background: 'none', color: extStatus.available ? T.accent : T.textFaint, opacity: extStatus.available ? 1 : 0.6,
              }}
            >
              <span>＋</span><span>{extStatus.available ? 'Connect a drive' : 'Connect (unavailable)'}</span>
            </button>
          </>
        )}

        {/* Import (copy provider files into your Drive) — distinct from Connect
            (mount). Disabled with a hint when the integration broker isn't set. */}
        <button
          onClick={() => importStatus.available && setImportOpen(true)}
          disabled={!importStatus.available}
          title={importStatus.available
            ? 'Import a copy of your Google / Microsoft files into your Drive'
            : 'Import requires a connected cloud account'}
          style={{
            display: 'flex', alignItems: 'center', gap: 9, padding: '9px 11px', borderRadius: 9, marginTop: 6,
            border: 'none', cursor: importStatus.available ? 'pointer' : 'not-allowed', fontSize: 13, textAlign: 'left',
            background: 'none', color: importStatus.available ? T.accent : T.textFaint, opacity: importStatus.available ? 1 : 0.6,
          }}
        >
          <span>⇩</span><span>{importStatus.available ? 'Import files' : 'Import (unavailable)'}</span>
        </button>

        <div style={{ marginTop: 'auto', paddingTop: 12 }}>
          <Btn small onClick={() => setRedeemOpen(true)}>↓ Redeem link</Btn>
        </div>
      </div>

      {/* main */}
      <div
        ref={dropRef}
        onDragOver={(e) => { e.preventDefault(); if (!readOnlyView) setDragOver(true) }}
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
          {extMountId && !extWritable && (
            <span style={{ fontSize: 11, color: T.textFaint, border: `1px solid ${T.border}`, borderRadius: 6, padding: '3px 8px' }}>Read-only</span>
          )}
          {canWrite && (
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
              <div style={{ fontSize: 40, opacity: 0.4 }}>{view === 'shared' ? '👥' : view === 'received' ? '📥' : '📂'}</div>
              <div style={{ color: T.textDim, fontSize: 14 }}>
                {view === 'received' ? 'Nothing received yet' : view === 'shared' ? 'Nothing shared with you yet' : extMountId ? (atRoot ? `${extMount?.name || 'This drive'} is empty` : 'This folder is empty') : atRoot ? 'Your Drive is empty' : 'This folder is empty'}
              </div>
              {view === 'received' && <Btn small primary onClick={() => setRedeemOpen(true)}>Redeem a link</Btn>}
              {canWrite && <Btn small primary onClick={() => fileInputRef.current?.click()}>Upload a file</Btn>}
            </Center>
          ) : view === 'received' ? (
            <div style={{ padding: 8 }}>
              {nodes.map((item) => (
                <div
                  key={item.id}
                  style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '9px 12px', borderRadius: 9 }}
                >
                  <span style={{ fontSize: 18, width: 22, textAlign: 'center', flexShrink: 0 }}>{item.is_dir ? '📁' : fileGlyph(item)}</span>
                  <span style={{ flex: 1, overflow: 'hidden', minWidth: 0 }}>
                    <span style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: T.text }}>{item.name}</span>
                    <span style={{ display: 'block', color: T.textFaint, fontSize: 11, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                      from {String(item.owner_vula_id || '').slice(0, 22)}… · {fmtDate(item.received_at)}
                    </span>
                  </span>
                  <span style={{ color: T.textFaint, fontSize: 12 }}>{item.is_dir ? 'folder' : fmtSize(item.size)}</span>
                  <Btn small onClick={() => { setBusy(`Downloading ${item.name}…`); downloadReceived(item).catch((e) => setError(e.message || 'Download failed')).finally(() => setBusy(null)) }}>Download</Btn>
                  {item.saved_node_id
                    ? <span style={{ color: T.textDim, fontSize: 12, width: 90, textAlign: 'center' }}>Saved ✓</span>
                    : <Btn small primary onClick={() => saveReceived(item)}>Save to Drive</Btn>}
                </div>
              ))}
            </div>
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
                    {!extMountId && (
                      <button
                        onClick={(e) => { e.stopPropagation(); setMenuFor(menuFor === node.id ? null : node.id) }}
                        style={{ background: 'none', border: 'none', color: T.textDim, cursor: 'pointer', fontSize: 18, lineHeight: 1, padding: '0 4px' }}
                      >⋯</button>
                    )}
                  </span>
                  {!extMountId && menuFor === node.id && (
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
            try {
              if (extMountId) await filesApi.externalFolder(extMountId, cur.id, name)
              else await filesApi.createFolder(cur.id, name)
              await closeModal(true)
            } catch (e) { setError(e.message || 'Create failed'); setModal(null) }
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
      {modal?.kind === 'peershare' && <PeerShareModal node={modal.node} onClose={() => setModal(null)} />}
      {modal?.kind === 'versions' && <VersionsModal node={modal.node} onClose={() => setModal(null)} />}
      {redeemOpen && <RedeemModal onClose={() => setRedeemOpen(false)} onRedeemed={() => { if (view === 'received') refresh() }} />}
      {connectOpen && <ConnectModal providers={extStatus.providers || []} onConnect={connectExternal} onClose={() => setConnectOpen(false)} />}
      {importOpen && (
        <ImportModal
          sources={importStatus.sources || []}
          jobs={importJobs}
          onStart={startImport}
          onSync={syncImport}
          onDelete={deleteImport}
          onClose={() => setImportOpen(false)}
        />
      )}

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
