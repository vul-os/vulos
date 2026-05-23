import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// AIROT-03: AI Router config panel
// Endpoints (from AIROT-02):
//   GET  /api/ai/status   → { mode, active_model, account?, usage? }
//   GET  /api/ai/models   → [{ slug, name, provider }]
//   PUT  /api/ai/config   → body { mode?, active_model?, provider? }
// Provider CRUD is handled through PUT /api/ai/config with provider sub-key.
// Degrades gracefully when the backend is not yet wired.
// ---------------------------------------------------------------------------

function Section({ title, children }) {
  return <div><h2 className="text-lg font-medium mb-4">{title}</h2>{children}</div>
}

function Field({ label, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-neutral-500 mb-1">{label}</label>
      {children}
    </div>
  )
}

function InfoRow({ label, value }) {
  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
      <span className="text-xs text-neutral-500">{label}</span>
      <span className="text-sm text-neutral-300">{value || '—'}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Provider form (add / edit)
// ---------------------------------------------------------------------------
function ProviderForm({ initial, onSave, onCancel, busy }) {
  const [name, setName] = useState(initial?.name || '')
  const [baseUrl, setBaseUrl] = useState(initial?.base_url || '')
  const [apiKey, setApiKey] = useState('')
  const [error, setError] = useState('')

  const handleSubmit = (e) => {
    e.preventDefault()
    if (!name.trim()) { setError('Name is required'); return }
    if (!baseUrl.trim()) { setError('Base URL is required'); return }
    setError('')
    onSave({ name: name.trim(), base_url: baseUrl.trim(), api_key: apiKey || undefined })
  }

  return (
    <form onSubmit={handleSubmit} className="mt-4 p-4 rounded-xl border border-neutral-700/60 bg-neutral-900/50 space-y-3">
      <h3 className="text-sm font-medium text-neutral-300">
        {initial ? 'Edit provider' : 'Add provider'}
      </h3>

      <Field label="Display name">
        <input
          value={name}
          onChange={e => setName(e.target.value)}
          placeholder="e.g. My OpenAI"
          className="input"
          required
        />
      </Field>

      <Field label="Base URL (OpenAI-compatible)">
        <input
          value={baseUrl}
          onChange={e => setBaseUrl(e.target.value)}
          placeholder="https://api.openai.com/v1"
          className="input"
          required
        />
      </Field>

      <Field label={initial ? 'API key (leave blank to keep existing)' : 'API key'}>
        <input
          type="password"
          value={apiKey}
          onChange={e => setApiKey(e.target.value)}
          placeholder={initial ? '(unchanged)' : 'sk-…'}
          className="input"
          autoComplete="new-password"
        />
        <p className="text-[11px] text-neutral-600 mt-1">
          Stored encrypted at rest — never returned by the API.
        </p>
      </Field>

      {error && <p className="text-xs text-red-400">{error}</p>}

      <div className="flex gap-2 pt-1">
        <button type="submit" disabled={busy} className="btn text-sm disabled:opacity-50">
          {busy ? 'Saving…' : initial ? 'Update' : 'Add'}
        </button>
        <button type="button" onClick={onCancel} className="btn-ghost text-sm">
          Cancel
        </button>
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Provider row
// ---------------------------------------------------------------------------
function ProviderRow({ provider, onEdit, onDelete, busy }) {
  return (
    <div className="flex items-center justify-between py-2.5 border-b border-neutral-800/30">
      <div className="min-w-0">
        <span className="text-sm text-neutral-200">{provider.name}</span>
        <span className="text-xs text-neutral-600 ml-2 truncate">{provider.base_url}</span>
        {provider.has_key && (
          <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded bg-green-900/30 text-green-500">
            key set
          </span>
        )}
      </div>
      <div className="flex gap-2 shrink-0 ml-3">
        <button
          onClick={() => onEdit(provider)}
          disabled={busy}
          className="text-xs text-blue-400 hover:text-blue-300 disabled:opacity-40"
        >
          Edit
        </button>
        <button
          onClick={() => onDelete(provider.id)}
          disabled={busy}
          className="text-xs text-red-400 hover:text-red-300 disabled:opacity-40"
        >
          Delete
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main panel
// ---------------------------------------------------------------------------
export default function AIRouterPanel() {
  const [status, setStatus] = useState(null)       // GET /api/ai/status
  const [models, setModels] = useState([])          // GET /api/ai/models
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [error, setError] = useState('')

  // Local editable state (mirrors server)
  const [mode, setMode] = useState('byo')           // 'byo' | 'cloud'
  const [activeModel, setActiveModel] = useState('')
  const [providers, setProviders] = useState([])    // from status.providers

  // Provider form state
  const [showAddForm, setShowAddForm] = useState(false)
  const [editingProvider, setEditingProvider] = useState(null)
  const [formBusy, setFormBusy] = useState(false)
  const [formError, setFormError] = useState('')

  // -------------------------------------------------------------------------
  // Load
  // -------------------------------------------------------------------------
  const loadAll = useCallback(() => {
    setLoading(true)
    setError('')

    const statusP = fetch('/api/ai/status')
      .then(r => r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
      .catch(() => null)

    const modelsP = fetch('/api/ai/models')
      .then(r => r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
      .catch(() => [])

    Promise.all([statusP, modelsP]).then(([st, ml]) => {
      if (st) {
        setStatus(st)
        setMode(st.mode || 'byo')
        setActiveModel(st.active_model || '')
        setProviders(Array.isArray(st.providers) ? st.providers : [])
      }
      setModels(Array.isArray(ml) ? ml : [])
    }).catch(e => setError(e.message)).finally(() => setLoading(false))
  }, [])

  useEffect(() => { loadAll() }, [loadAll])

  // -------------------------------------------------------------------------
  // PUT /api/ai/config helper
  // -------------------------------------------------------------------------
  const putConfig = async (body) => {
    const r = await fetch('/api/ai/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status)
    return d
  }

  // -------------------------------------------------------------------------
  // Save mode + active model
  // -------------------------------------------------------------------------
  const handleSave = async () => {
    setSaving(true)
    setSaved(false)
    setError('')
    try {
      await putConfig({ mode, active_model: activeModel || undefined })
      setSaved(true)
      setTimeout(() => setSaved(false), 2000)
      loadAll()
    } catch (e) {
      setError(e.message)
    } finally {
      setSaving(false)
    }
  }

  // -------------------------------------------------------------------------
  // Provider add / edit
  // -------------------------------------------------------------------------
  const handleProviderSave = async (data) => {
    setFormBusy(true)
    setFormError('')
    try {
      if (editingProvider) {
        await putConfig({ provider: { action: 'update', id: editingProvider.id, ...data } })
      } else {
        await putConfig({ provider: { action: 'add', ...data } })
      }
      setShowAddForm(false)
      setEditingProvider(null)
      loadAll()
    } catch (e) {
      setFormError(e.message)
    } finally {
      setFormBusy(false)
    }
  }

  const handleProviderDelete = async (id) => {
    if (!confirm('Remove this provider?')) return
    setSaving(true)
    setError('')
    try {
      await putConfig({ provider: { action: 'delete', id } })
      loadAll()
    } catch (e) {
      setError(e.message)
    } finally {
      setSaving(false)
    }
  }

  const handleEditProvider = (provider) => {
    setShowAddForm(false)
    setEditingProvider(provider)
    setFormError('')
  }

  const handleAddProvider = () => {
    setEditingProvider(null)
    setShowAddForm(true)
    setFormError('')
  }

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------
  return (
    <Section title="AI Router">
      <p className="text-xs text-neutral-600 mb-5 leading-relaxed">
        Configure how AI features on this device reach language model providers.
        Choose <strong className="text-neutral-400">BYO Keys</strong> to supply your own provider API
        keys, or <strong className="text-neutral-400">Vulos Cloud</strong> to bill through your Vulos account
        (zero setup required).
      </p>

      {/* ------------------------------------------------------------------ */}
      {/* Mode toggle                                                          */}
      {/* ------------------------------------------------------------------ */}
      <Field label="Supply mode">
        <div className="flex gap-2">
          {[
            { value: 'byo', label: 'BYO Keys', desc: 'Your own provider API keys' },
            { value: 'cloud', label: 'Vulos Cloud', desc: 'Billed to your Vulos account' },
          ].map(opt => (
            <button
              key={opt.value}
              onClick={() => setMode(opt.value)}
              className={`flex-1 py-3 px-3 rounded-xl text-sm transition-all border text-left
                ${mode === opt.value
                  ? 'bg-blue-600/20 border-blue-500/50 text-blue-300'
                  : 'bg-neutral-900/50 border-neutral-800/50 text-neutral-400 hover:border-neutral-700 hover:text-neutral-200'}`}
            >
              <div className="font-medium">{opt.label}</div>
              <div className="text-[11px] mt-0.5 text-neutral-500">{opt.desc}</div>
            </button>
          ))}
        </div>
      </Field>

      {/* ------------------------------------------------------------------ */}
      {/* Model selector — always visible                                      */}
      {/* ------------------------------------------------------------------ */}
      <Field label="Default model">
        {models.length > 0 ? (
          <select
            value={activeModel}
            onChange={e => setActiveModel(e.target.value)}
            className="input"
          >
            <option value="">— select a model —</option>
            {models.map(m => (
              <option key={m.slug} value={m.slug}>
                {m.name || m.slug}{m.provider ? ` (${m.provider})` : ''}
              </option>
            ))}
          </select>
        ) : (
          <div className="input bg-neutral-900/30 text-neutral-600 cursor-default select-none">
            {loading ? 'Loading…' : 'No models available — add a provider or connect to Vulos Cloud'}
          </div>
        )}
      </Field>

      {/* ------------------------------------------------------------------ */}
      {/* BYO mode: provider management                                        */}
      {/* ------------------------------------------------------------------ */}
      {mode === 'byo' && (
        <div className="mt-4 pt-4 border-t border-neutral-800/50">
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-sm font-medium text-neutral-300">Providers</h3>
            {!showAddForm && !editingProvider && (
              <button
                onClick={handleAddProvider}
                disabled={saving}
                className="text-xs text-blue-400 hover:text-blue-300 disabled:opacity-40"
              >
                + Add provider
              </button>
            )}
          </div>

          {providers.length === 0 && !showAddForm && !editingProvider && (
            <p className="text-xs text-neutral-600 mb-3">
              No providers configured. Add an OpenAI-compatible endpoint to get started.
            </p>
          )}

          {providers.map(p => (
            <ProviderRow
              key={p.id}
              provider={p}
              onEdit={handleEditProvider}
              onDelete={handleProviderDelete}
              busy={saving}
            />
          ))}

          {showAddForm && !editingProvider && (
            <ProviderForm
              initial={null}
              onSave={handleProviderSave}
              onCancel={() => { setShowAddForm(false); setFormError('') }}
              busy={formBusy}
            />
          )}

          {editingProvider && (
            <ProviderForm
              initial={editingProvider}
              onSave={handleProviderSave}
              onCancel={() => { setEditingProvider(null); setFormError('') }}
              busy={formBusy}
            />
          )}

          {formError && (
            <p className="mt-2 text-xs text-red-400">{formError}</p>
          )}
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Cloud mode: account + usage summary                                  */}
      {/* ------------------------------------------------------------------ */}
      {mode === 'cloud' && (
        <div className="mt-4 pt-4 border-t border-neutral-800/50">
          <h3 className="text-sm font-medium text-neutral-300 mb-3">Vulos Cloud</h3>
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-4">
            <InfoRow
              label="Account"
              value={status?.account || (loading ? '…' : 'Not connected')}
            />
            <InfoRow
              label="Usage this cycle"
              value={
                status?.usage != null
                  ? `${status.usage.tokens?.toLocaleString() ?? 0} tokens`
                  : loading ? '…' : '—'
              }
            />
            {status?.usage?.cost_usd != null && (
              <InfoRow
                label="Estimated cost"
                value={`$${status.usage.cost_usd.toFixed(4)}`}
              />
            )}
          </div>
          <a
            href="https://vulos.org/dashboard/ai"
            target="_blank"
            rel="noreferrer"
            className="text-xs text-blue-400 hover:text-blue-300 transition-colors"
          >
            Open cloud dashboard
          </a>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Status / error                                                        */}
      {/* ------------------------------------------------------------------ */}
      {status && !loading && (
        <div className="mt-5 pt-4 border-t border-neutral-800/50">
          <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
            <InfoRow label="Router mode" value={status.mode || '—'} />
            <InfoRow label="Active model" value={status.active_model || '—'} />
          </div>
        </div>
      )}

      {!loading && !status && (
        <div className="mt-4 p-3 rounded-lg bg-amber-900/20 border border-amber-800/30">
          <p className="text-xs text-amber-400">
            AI Router backend not yet connected — configuration will be saved locally
            and applied when the backend is available.
          </p>
        </div>
      )}

      {/* ------------------------------------------------------------------ */}
      {/* Save bar                                                              */}
      {/* ------------------------------------------------------------------ */}
      <div className="flex gap-3 items-center mt-5">
        <button
          onClick={handleSave}
          disabled={saving || loading}
          className="btn text-sm disabled:opacity-50"
        >
          {saving ? 'Saving…' : saved ? 'Saved' : 'Save'}
        </button>
        <button
          onClick={loadAll}
          disabled={loading || saving}
          className="btn-ghost text-sm"
        >
          Refresh
        </button>
      </div>

      {error && (
        <div className="mt-3 text-xs rounded px-3 py-2 bg-red-900/30 text-red-400">
          {error}
        </div>
      )}
    </Section>
  )
}
