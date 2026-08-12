/**
 * shareToPeer.js — pure helpers for the Files "Share to peer" flow.
 *
 * The Files app hands a selected file (or folder) to the peering "Drop"
 * transport: a LAN-first / internet-fallback, signed-URL + content-hash
 * pipeline. We do NOT reinvent the wire protocol here — we only translate a
 * Files selection into the `/api/peering/drop/send` request that the backend
 * DropService already understands.
 *
 * The protocol lives in the backend (backend/services/peering/drop*.go); Files
 * is now its only client. The old AirDrop-style Drop panel that used to be the
 * reference implementation was retired with the rest of the orphaned peering UI
 * — it had lost its entry point when Messages replaced Peering in the builtin
 * registry, and comms are third-party per the product standard.
 *
 * Kept dependency-free and side-effect-free so the path/MIME logic can be
 * unit-tested without a DOM or a backend.
 */

/**
 * Resolve a Files path (which may be `~`-relative) to an absolute path the
 * box backend can `os.Stat`. The Drop send endpoint expects an absolute
 * `media_path`; the shell's `~` is never expanded by Go's os.Stat, so we must
 * substitute the resolved home directory ourselves.
 *
 * @param path  e.g. "~/Documents/report.pdf" or "/tmp/x.txt"
 * @param home  the resolved $HOME, e.g. "/home/vulos"
 * @returns an absolute path
 */
export function resolveAbsPath(path: string, home: string | null | undefined): string {
  if (!path) return path
  if (path === '~') return home || path
  if (path.startsWith('~/') && home) return home + path.slice(1)
  return path
}

/** Extension -> MIME guess. Mirrors the Drop panel's octet-stream default. */
const MIME_BY_EXT: Record<string, string> = {
  png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif',
  webp: 'image/webp', svg: 'image/svg+xml', ico: 'image/x-icon',
  mp4: 'video/mp4', mov: 'video/quicktime', webm: 'video/webm', mkv: 'video/x-matroska',
  mp3: 'audio/mpeg', wav: 'audio/wav', flac: 'audio/flac', ogg: 'audio/ogg',
  pdf: 'application/pdf', zip: 'application/zip', gz: 'application/gzip',
  tar: 'application/x-tar', json: 'application/json', txt: 'text/plain',
  md: 'text/markdown', csv: 'text/csv', html: 'text/html',
}

/**
 * Guess a MIME type from a filename. Returns application/octet-stream when
 * unknown — matching the backend's default so the transfer still succeeds.
 */
export function guessMime(name: string): string {
  if (!name || !name.includes('.')) return 'application/octet-stream'
  const ext = (name.split('.').pop() || '').toLowerCase()
  return MIME_BY_EXT[ext] || 'application/octet-stream'
}

/** Minimal peer shape needed to address a Drop send — see SharePeerModal's
 *  roster entries from GET /api/peering/drop/nearby. */
export interface SharePeer {
  vulos_id: string
  addr?: string
}

/** Body for POST /api/peering/drop/send — mirrors what Drop.jsx posts, so the
 *  same DropService handler serves both surfaces. */
export interface DropSendBody {
  target_vulos_id: string
  media_path: string
  mime_type: string
  target_addr?: string
}

/**
 * Build the JSON body for POST /api/peering/drop/send from a chosen peer and a
 * resolved absolute media path. Mirrors the shape Drop.jsx posts, so the same
 * DropService handler serves both surfaces.
 */
export function buildSendBody(peer: SharePeer, absPath: string, mimeType: string): DropSendBody {
  const body: DropSendBody = {
    target_vulos_id: peer.vulos_id,
    media_path: absPath,
    mime_type: mimeType || 'application/octet-stream',
  }
  // Prefer the peer's advertised LAN address when known (matches Drop.jsx);
  // otherwise the backend resolves it from its own nearby list.
  if (peer.addr) body.target_addr = `http://${peer.addr}`
  return body
}

/**
 * Split an absolute path into { parent, base } for `tar -C <parent> <base>`.
 */
export function splitPath(absPath: string): { parent: string; base: string } {
  const idx = absPath.lastIndexOf('/')
  if (idx < 0) return { parent: '.', base: absPath }
  if (idx === 0) return { parent: '/', base: absPath.slice(1) }
  return { parent: absPath.slice(0, idx), base: absPath.slice(idx + 1) }
}

/**
 * Build the shell command that archives a folder into a temp `.tar.gz` so it
 * can be sent as a single blob over the Drop transport (which moves files, not
 * trees). The archive basename keeps the folder's name so the recipient sees
 * "<folder>.tar.gz" land in Downloads.
 *
 * Returns { command, archivePath }.
 */
export function buildFolderArchiveCommand(absPath: string): { command: string; archivePath: string } {
  const { parent, base } = splitPath(absPath)
  // A per-share temp dir keeps the archive basename == "<name>.tar.gz".
  const tmpDir = `/tmp/.vulos-share-${Date.now()}`
  const archivePath = `${tmpDir}/${base}.tar.gz`
  const q = (s: string) => `"${String(s).replace(/"/g, '\\"')}"`
  const command =
    `mkdir -p ${q(tmpDir)} && tar -czf ${q(archivePath)} -C ${q(parent)} ${q(base)} && echo OK`
  return { command, archivePath }
}
