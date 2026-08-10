import { useState, useEffect, useCallback, useRef, type ReactNode, type ChangeEvent } from 'react'
import { useAuth } from '../../auth/AuthProvider'

// ---------------------------------------------------------------------------
// Private-AI model management (owner-only).
//
// Surfaces + manages what REALLY exists on the box's AI seam:
//   • the local embeddings/RAG model dir (which .onnx is installed, and whether
//     the model's real tokenizer.json is present → the honest difference between
//     genuine semantic RAG and the degraded FNV-lexical fallback), and
//   • the chat models the llmux gateway exposes (best-effort; llmux owns chat
//     provider/model routing).
//
// The IMPORT flow is how an operator installs the tokenizer.json that upgrades
// retrieval from the degraded fallback to real semantic RAG.
//
// Endpoints:
//   GET  /api/models          → { embeddings: Listing, chat_models, chat_models_error? }
//   POST /api/models/import   → multipart { kind: model|tokenizer, artifact } → { imported, embeddings }
//
// Both are owner-gated server-side (403 for non-owners); this panel additionally
// hides itself for non-owners so the surface stays owner-facing.
// ---------------------------------------------------------------------------

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}
function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

function Section({ title, desc, children }: { title: ReactNode; desc?: ReactNode; children?: ReactNode }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed max-w-[68ch]">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

function humanBytes(n: number | undefined): string {
  if (!n || n < 0) return '—'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 && i > 0 ? 1 : 0)} ${u[i]}`
}

// ── Wire shapes, narrowed field-by-field (see backend/services/models/models.go
// + catalog.go for the authoritative JSON these mirror) ──────────────────────

type RagMode = 'semantic' | 'degraded' | 'lexical'

interface PythonDepsStatus {
  python3?: boolean
  onnxruntime?: boolean
  tokenizers?: boolean
  ready?: boolean
  install_hint?: string
}
function toPythonDeps(x: unknown): PythonDepsStatus | undefined {
  if (!isRecord(x)) return undefined
  return {
    python3: typeof x.python3 === 'boolean' ? x.python3 : undefined,
    onnxruntime: typeof x.onnxruntime === 'boolean' ? x.onnxruntime : undefined,
    tokenizers: typeof x.tokenizers === 'boolean' ? x.tokenizers : undefined,
    ready: typeof x.ready === 'boolean' ? x.ready : undefined,
    install_hint: typeof x.install_hint === 'string' ? x.install_hint : undefined,
  }
}

interface LocalModel {
  name: string
  size_bytes?: number
  sha256?: string
  has_tokenizer?: boolean
  active?: boolean
}
function toLocalModel(x: unknown): LocalModel | null {
  if (!isRecord(x) || typeof x.name !== 'string') return null
  return {
    name: x.name,
    size_bytes: typeof x.size_bytes === 'number' ? x.size_bytes : undefined,
    sha256: typeof x.sha256 === 'string' ? x.sha256 : undefined,
    has_tokenizer: typeof x.has_tokenizer === 'boolean' ? x.has_tokenizer : undefined,
    active: typeof x.active === 'boolean' ? x.active : undefined,
  }
}

interface PinnedArtifact {
  size_bytes?: number
}
function toPinnedArtifact(x: unknown): PinnedArtifact | undefined {
  if (!isRecord(x)) return undefined
  return { size_bytes: typeof x.size_bytes === 'number' ? x.size_bytes : undefined }
}

interface CatalogEntry {
  id: string
  name: string
  description?: string
  dim?: number
  recommended?: boolean
  model?: PinnedArtifact
  tokenizer?: PinnedArtifact
}
function toCatalogEntry(x: unknown): CatalogEntry | null {
  if (!isRecord(x) || typeof x.id !== 'string' || typeof x.name !== 'string') return null
  return {
    id: x.id,
    name: x.name,
    description: typeof x.description === 'string' ? x.description : undefined,
    dim: typeof x.dim === 'number' ? x.dim : undefined,
    recommended: typeof x.recommended === 'boolean' ? x.recommended : undefined,
    model: toPinnedArtifact(x.model),
    tokenizer: toPinnedArtifact(x.tokenizer),
  }
}

interface ModelsListing {
  dir?: string
  models?: LocalModel[]
  rag_mode?: RagMode
  catalog?: CatalogEntry[]
  python_deps?: PythonDepsStatus
}
function toModelsListing(x: unknown): ModelsListing | undefined {
  if (!isRecord(x)) return undefined
  const ragMode = x.rag_mode
  return {
    dir: typeof x.dir === 'string' ? x.dir : undefined,
    models: Array.isArray(x.models) ? x.models.map(toLocalModel).filter((m): m is LocalModel => m !== null) : undefined,
    rag_mode: ragMode === 'semantic' || ragMode === 'degraded' || ragMode === 'lexical' ? ragMode : undefined,
    catalog: Array.isArray(x.catalog) ? x.catalog.map(toCatalogEntry).filter((c): c is CatalogEntry => c !== null) : undefined,
    python_deps: toPythonDeps(x.python_deps),
  }
}

interface ImportResult {
  name?: string
  size_bytes?: number
}
function toImportResult(x: unknown): ImportResult | undefined {
  if (!isRecord(x)) return undefined
  return {
    name: typeof x.name === 'string' ? x.name : undefined,
    size_bytes: typeof x.size_bytes === 'number' ? x.size_bytes : undefined,
  }
}

interface ModelsData {
  embeddings?: ModelsListing
  chat_models?: unknown
  chat_models_error?: string
}
function toModelsData(x: unknown): ModelsData {
  if (!isRecord(x)) return {}
  return {
    embeddings: toModelsListing(x.embeddings),
    chat_models: x.chat_models,
    chat_models_error: typeof x.chat_models_error === 'string' ? x.chat_models_error : undefined,
  }
}

interface ImportResponse {
  imported?: ImportResult
  embeddings?: ModelsListing
}
function toImportResponse(x: unknown): ImportResponse {
  if (!isRecord(x)) return {}
  return { imported: toImportResult(x.imported), embeddings: toModelsListing(x.embeddings) }
}

interface DownloadResponse {
  embeddings?: ModelsListing
}
function toDownloadResponse(x: unknown): DownloadResponse {
  if (!isRecord(x)) return {}
  return { embeddings: toModelsListing(x.embeddings) }
}

// RAG_MODES describes the three honest retrieval states the assistant can be in.
const RAG_MODES: Record<RagMode, { label: string; tone: string; dot: string; desc: string }> = {
  semantic: {
    label: 'Semantic RAG active',
    tone: 'text-[var(--status-success)] bg-[var(--status-success-soft)] border-success-soft',
    dot: 'bg-[var(--status-success)]',
    desc: 'A local embedding model and its real tokenizer.json are installed. Your assistant retrieves mail by meaning — genuine semantic search, entirely on your box.',
  },
  degraded: {
    label: 'Degraded fallback',
    tone: 'text-[var(--status-warning)] bg-[var(--status-warning-soft)] border-warning-soft',
    dot: 'bg-[var(--status-warning)]',
    desc: 'A local model is installed but its tokenizer.json is missing, so embeddings use a deterministic hash fallback. Vectors are reproducible but only weakly meaningful — retrieval quality is reduced. Import the model’s tokenizer.json below to upgrade to real semantic RAG.',
  },
  lexical: {
    label: 'Lexical retrieval',
    tone: 'text-[var(--text-secondary)] bg-[var(--bg-elevated)] border-[var(--border-strong)]',
    dot: 'bg-[var(--bg-active)]',
    desc: 'No local embedding model is installed, so the assistant uses on-box lexical (keyword) retrieval. This is fully sovereign and needs no model, but does not find messages by meaning. Import an .onnx embedding model below to enable semantic RAG.',
  },
}

// RAGReadinessBadge — the honest indicator of the assistant's retrieval quality.
function RAGReadinessBadge({ mode }: { mode: RagMode }) {
  const m = RAG_MODES[mode] || RAG_MODES.lexical
  return (
    <div className={`rounded-xl border px-4 py-3 ${m.tone}`}>
      <div className="flex items-center gap-2">
        <span className={`inline-block w-2 h-2 rounded-full ${m.dot}`} aria-hidden="true" />
        <span className="text-sm font-medium">{m.label}</span>
      </div>
      <p className="text-xs mt-1.5 leading-relaxed opacity-90">{m.desc}</p>
    </div>
  )
}

// PythonDepsNotice reports whether the on-box python embedding runtime
// (onnxruntime + tokenizers) is present. Without it, a model + tokenizer can be
// installed yet every embed call fails and retrieval silently degrades — so we
// surface the honest state and the exact install command. We NEVER auto-install.
function PythonDepsNotice({ deps }: { deps: PythonDepsStatus | undefined }) {
  if (!deps) return null
  const hint = deps.install_hint || 'pip install onnxruntime tokenizers numpy'
  if (deps.ready) {
    return (
      <div className="mb-3 text-[12px] leading-relaxed rounded-lg px-3 py-2 bg-[var(--status-success-soft)] text-[var(--status-success)] border border-success-soft">
        On-box embedding runtime detected (python3 + onnxruntime + tokenizers). Semantic embeddings can run locally.
      </div>
    )
  }
  const missing: string[] = []
  if (!deps.python3) missing.push('python3')
  if (!deps.onnxruntime) missing.push('onnxruntime')
  if (!deps.tokenizers) missing.push('tokenizers')
  return (
    <div className="mb-3 text-[12px] leading-relaxed rounded-lg px-3 py-2 bg-[var(--status-warning-soft)] text-[var(--status-warning)] border border-warning-soft">
      The on-box embedding runtime is missing ({missing.join(', ') || 'dependencies'}). A model can be installed, but
      embeddings will not run until you install the <strong>vula-embed</strong> Python deps on the box:
      <code className="block mt-1.5 font-mono text-[var(--status-warning)] bg-[var(--bg-base)] rounded px-2 py-1 select-all">{hint}</code>
      This is never installed automatically — run it yourself on the box.
    </div>
  )
}

// CatalogRow offers a one-click download of a CURATED, PINNED model. The bytes
// are fetched on demand from the pinned huggingface.co URL, SHA-256-verified
// fail-closed, and installed atomically — the model binary is never shipped in
// the app. On success the RAG readiness badge flips to semantic.
function CatalogRow({ entry, onDownloaded }: { entry: CatalogEntry; onDownloaded?: (d: DownloadResponse) => void }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')

  const totalBytes = (entry.model?.size_bytes || 0) + (entry.tokenizer?.size_bytes || 0)

  const download = async () => {
    setBusy(true); setErr(''); setOk('')
    try {
      const res = await fetch('/api/models/download', {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: entry.id }),
      })
      const data: unknown = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error((isRecord(data) && typeof data.error === 'string' && data.error) || `HTTP ${res.status}`)
      setOk(`Installed ${entry.name} (${humanBytes(totalBytes)}) — model + tokenizer verified.`)
      onDownloaded?.(toDownloadResponse(data))
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-lg border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-sm font-medium text-[var(--text-primary)] font-mono">{entry.name}</span>
            {entry.recommended && (
              <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--accent-soft)] text-[var(--accent)]">Recommended</span>
            )}
            <span className="text-[12px] px-1.5 py-0.5 rounded bg-[var(--bg-elevated)] text-[var(--text-tertiary)]">{entry.dim}-dim</span>
          </div>
          <p className="text-xs text-[var(--text-muted)] mt-0.5 leading-relaxed">{entry.description}</p>
          <p className="text-[12px] text-[var(--text-faint)] mt-0.5">Download: {humanBytes(totalBytes)} (model + tokenizer), verified by SHA-256.</p>
        </div>
        <button onClick={download} disabled={busy} aria-busy={busy} className="btn text-sm shrink-0 disabled:opacity-50 flex items-center gap-1.5">
          {busy && <span className="spinner w-3.5 h-3.5" aria-hidden="true" />}
          {busy ? 'Downloading…' : 'Download'}
        </button>
      </div>
      {busy && <div className="mt-2 text-[12px] text-[var(--text-muted)]" role="status">Fetching + verifying the pinned model — this can take a minute on first install.</div>}
      {ok && <div className="mt-2 text-xs rounded px-3 py-1.5 bg-[var(--status-success-soft)] text-[var(--status-success)]" role="status">{ok}</div>}
      {err && <div className="mt-2 text-xs rounded px-3 py-1.5 bg-[var(--status-danger-soft)] text-[var(--status-danger)]" role="alert">{err}</div>}
    </div>
  )
}

function ImportRow({ kind, label, accept, hint, onImported }: {
  kind: 'model' | 'tokenizer'
  label: string
  accept: string
  hint: string
  onImported?: (d: ImportResponse) => void
}) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')

  const pick = () => { setErr(''); setOk(''); inputRef.current?.click() }

  const onFile = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = '' // allow re-selecting the same file
    if (!file) return
    setBusy(true); setErr(''); setOk('')
    try {
      const fd = new FormData()
      fd.append('kind', kind)
      fd.append('artifact', file)
      const res = await fetch('/api/models/import', {
        method: 'POST',
        credentials: 'include',
        body: fd, // browser sets multipart Content-Type + boundary
      })
      const raw: unknown = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error((isRecord(raw) && typeof raw.error === 'string' && raw.error) || `HTTP ${res.status}`)
      const data = toImportResponse(raw)
      setOk(`Installed ${data.imported?.name || 'artifact'} (${humanBytes(data.imported?.size_bytes)})`)
      onImported?.(data)
    } catch (e2) {
      setErr(errorMessage(e2))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-lg border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium text-[var(--text-primary)]">{label}</div>
          <p className="text-xs text-[var(--text-muted)] mt-0.5 leading-relaxed">{hint}</p>
        </div>
        <button onClick={pick} disabled={busy} aria-busy={busy} className="btn text-sm shrink-0 disabled:opacity-50 flex items-center gap-1.5">
          {busy && <span className="spinner w-3.5 h-3.5" aria-hidden="true" />}
          {busy ? 'Importing…' : 'Import…'}
        </button>
      </div>
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        onChange={onFile}
        className="hidden"
        aria-label={`Import ${label}`}
      />
      {ok && <div className="mt-2 text-xs rounded px-3 py-1.5 bg-[var(--status-success-soft)] text-[var(--status-success)]" role="status">{ok}</div>}
      {err && <div className="mt-2 text-xs rounded px-3 py-1.5 bg-[var(--status-danger-soft)] text-[var(--status-danger)]" role="alert">{err}</div>}
    </div>
  )
}

// ModelsPanel gates on ownership at RENDER time, then delegates to the
// owner-only body. Non-owners never mount the data/effect logic, so they never
// hit the (owner-gated) endpoint at all — the server independently 403s it.
export default function ModelsPanel() {
  const { profile } = useAuth()
  const isOwner = profile?.role === 'admin'
  if (!isOwner) {
    return (
      <Section title="AI Models">
        <p className="text-sm text-[var(--text-muted)]">Model management is available to the box owner only.</p>
      </Section>
    )
  }
  return <ModelsPanelOwner />
}

function ModelsPanelOwner() {
  const [data, setData] = useState<ModelsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  // load fetches the model-management view. All state writes happen in async
  // callbacks (not synchronously in an effect body) so a Refresh click re-runs
  // it identically to the initial load.
  const load = useCallback(() => {
    fetch('/api/models', { credentials: 'include' })
      .then(r => r.status === 403
        ? Promise.reject(new Error('Model management is available to the box owner only.'))
        : r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
      .then((d: unknown) => { setData(toModelsData(d)); setError('') })
      .catch((e: unknown) => setError(errorMessage(e)))
      .finally(() => setLoading(false))
  }, [])

  useEffect(() => { load() }, [load])

  const emb = data?.embeddings
  const ragMode = emb?.rag_mode || 'lexical'
  const chatModels = parseChatModels(data?.chat_models)

  return (
    <Section title="AI Models">
      <p className="text-xs text-[var(--text-faint)] mb-5 leading-relaxed">
        Manage the private AI running on <strong className="text-[var(--text-tertiary)]">your own box</strong>.
        The embedding model powers semantic search over your mail (RAG); chat models
        are routed through your llmux gateway. Everything here reflects what is
        actually installed — no hidden downloads.
      </p>

      {/* -------------------------------------------------------------- */}
      {/* RAG readiness — the honest retrieval-quality indicator          */}
      {/* -------------------------------------------------------------- */}
      <div className="mb-6">
        <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Retrieval quality</h3>
        <RAGReadinessBadge mode={ragMode} />
      </div>

      {loading && (
        <p className="text-sm text-[var(--text-muted)] flex items-center gap-2" role="status">
          <span className="spinner w-3.5 h-3.5" aria-hidden="true" />
          Loading models…
        </p>
      )}
      {error && (
        <div className="mb-4 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-[var(--status-danger)]" role="alert">{error}</div>
      )}

      {emb && (
        <>
          {/* Installed embedding models */}
          <div className="mb-6">
            <div className="flex items-center justify-between mb-2">
              <h3 className="text-sm font-medium text-[var(--text-secondary)]">Embedding models</h3>
              <button onClick={load} className="btn-ghost text-xs">Refresh</button>
            </div>
            {emb.models?.length ? (
              <div className="space-y-2">
                {emb.models.map(mm => (
                  <div key={mm.name} className="rounded-lg border border-[var(--border-default)] bg-[var(--bg-surface)] px-4 py-3">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="text-sm font-medium text-[var(--text-primary)] font-mono">{mm.name}</span>
                      {mm.active && (
                        <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--status-success-soft)] text-[var(--status-success)]">Active</span>
                      )}
                      {mm.has_tokenizer
                        ? <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--status-success-soft)] text-[var(--status-success)]">tokenizer ✓</span>
                        : <span className="text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-[var(--status-warning-soft)] text-[var(--status-warning)]">no tokenizer</span>}
                    </div>
                    <div className="text-xs text-[var(--text-muted)] mt-1">
                      {humanBytes(mm.size_bytes)}
                      {mm.sha256 && <span className="font-mono ml-2 opacity-70">sha256:{mm.sha256.slice(0, 12)}…</span>}
                    </div>
                  </div>
                ))}
              </div>
            ) : (
              <p className="text-sm text-[var(--text-muted)]">
                No embedding model installed — the assistant is using sovereign lexical retrieval.
                Import an <span className="font-mono">.onnx</span> model below to enable semantic RAG.
              </p>
            )}
            <p className="text-[12px] text-[var(--text-faint)] mt-2 font-mono truncate" title={emb.dir}>
              {emb.dir}
            </p>
          </div>

          {/* One-click download from the curated, pinned catalog */}
          {emb.catalog && emb.catalog.length > 0 && (
            <div className="mb-6">
              <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Download a recommended model</h3>
              <p className="text-[12px] text-[var(--text-faint)] mb-3 leading-relaxed">
                Install a curated embedding model in one click. The model is fetched on demand
                from a pinned source and verified by SHA-256 before it is installed — nothing
                is bundled in the app.
              </p>
              <PythonDepsNotice deps={emb.python_deps} />
              <div className="space-y-2">
                {emb.catalog.map(entry => (
                  <CatalogRow
                    key={entry.id}
                    entry={entry}
                    onDownloaded={(d) => { if (d.embeddings) setData(prev => ({ ...prev, embeddings: d.embeddings })) }}
                  />
                ))}
              </div>
            </div>
          )}

          {/* Import flow (manual) */}
          <div className="mb-6">
            <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Or import a model you already have</h3>
            <div className="mb-3 text-[12px] leading-relaxed rounded-lg px-3 py-2 bg-[var(--bg-elevated)] text-[var(--text-tertiary)] border border-[var(--border-strong)]">
              Already have an ONNX sentence-embedding model and its
              <span className="font-mono"> tokenizer.json</span>? Import both files here.
              The active model is picked up automatically.
            </div>
            <div className="space-y-2">
              <ImportRow
                kind="model"
                label="Embedding model (.onnx)"
                accept=".onnx"
                hint="A local ONNX sentence-embedding model. Installed as model.onnx and used for on-box semantic search."
                onImported={(d) => { if (d.embeddings) setData(prev => ({ ...prev, embeddings: d.embeddings })) }}
              />
              <ImportRow
                kind="tokenizer"
                label="Tokenizer (tokenizer.json)"
                accept=".json,application/json"
                hint="The model's real tokenizer. Without it, embeddings fall back to a weak hash — installing it upgrades RAG to genuine semantic retrieval."
                onImported={(d) => { if (d.embeddings) setData(prev => ({ ...prev, embeddings: d.embeddings })) }}
              />
            </div>
          </div>

          {/* Chat models (llmux) */}
          <div>
            <h3 className="text-sm font-medium text-[var(--text-secondary)] mb-2">Chat models</h3>
            {data?.chat_models_error ? (
              <p className="text-sm text-[var(--text-muted)]">{data.chat_models_error}</p>
            ) : chatModels.length ? (
              <div className="flex flex-wrap gap-2">
                {chatModels.map(id => (
                  <span key={id} className="text-xs font-mono rounded px-2 py-1 bg-[var(--bg-elevated)] text-[var(--text-secondary)]">{id}</span>
                ))}
              </div>
            ) : (
              <p className="text-sm text-[var(--text-muted)]">No chat models reported by the gateway.</p>
            )}
            <p className="text-[12px] text-[var(--text-faint)] mt-2 leading-relaxed">
              Chat model routing and provider management are handled by your llmux gateway.
            </p>
          </div>
        </>
      )}
    </Section>
  )
}

// parseChatModels normalizes the llmux /v1/models proxy payload (OpenAI shape
// { data: [{ id }] }) into a flat list of ids. Tolerates a null/odd payload.
function parseChatModels(raw: unknown): string[] {
  if (!raw) return []
  try {
    const arr = isRecord(raw) && Array.isArray(raw.data) ? raw.data : Array.isArray(raw) ? raw : []
    return arr
      .map((m: unknown) => (typeof m === 'string' ? m : isRecord(m) && typeof m.id === 'string' ? m.id : undefined))
      .filter((id): id is string => typeof id === 'string' && id.length > 0)
  } catch {
    return []
  }
}
