import { useState, useEffect, useCallback } from 'react'

// ---------------------------------------------------------------------------
// PUBWEB-07: Custom domains for published apps
// Endpoints (backend/services/appnet/customdomain_api.go):
//   GET    /api/apps/visibility          → [{app_id, visibility}] (app picker)
//   GET    /api/apps/{id}/domain         → current CustomDomain record (404 if none)
//   POST   /api/apps/{id}/domain         → {domain} attach; issues TXT challenge
//   POST   /api/apps/{id}/domain/verify  → live DNS TXT check; activates on success
//   DELETE /api/apps/{id}/domain         → detach; reverts to default subdomain
//
// An app must already be published (visibility:"public") before a domain can
// be attached — the backend 409s otherwise, so the picker only offers public
// apps and explains the prerequisite when there are none.
// ---------------------------------------------------------------------------

function Section({ title, desc, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

function Field({ label, hint, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      {children}
      {hint && <p className="text-[12px] text-[var(--text-faint)] mt-1">{hint}</p>}
    </div>
  )
}

function InfoRow({ label, value, mono }) {
  return (
    <div className="flex items-center justify-between gap-3 px-4 py-2.5 bg-[var(--bg-surface)]">
      <span className="text-xs text-[var(--text-muted)] shrink-0">{label}</span>
      <span className={`text-sm text-[var(--text-secondary)] text-right truncate ${mono ? 'font-mono' : ''}`}>
        {value || '—'}
      </span>
    </div>
  )
}

function StatusBadge({ status }) {
  const verified = status === 'verified'
  return (
    <span
      className={`text-[12px] uppercase tracking-wide px-1.5 py-0.5 rounded ${
        verified
          ? 'bg-[var(--status-success-soft)] text-[var(--status-success)]'
          : 'bg-[var(--status-warning-soft)] text-[var(--status-warning)]'
      }`}
    >
      {verified ? 'Verified' : 'Pending'}
    </span>
  )
}

// CopyField — a read-only mono field with a one-click copy button, used for
// the TXT record name/value the owner must paste into their DNS provider.
function CopyField({ label, value }) {
  const [copied, setCopied] = useState(false)
  const handleCopy = () => {
    navigator.clipboard?.writeText(value || '').catch(() => {})
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      <div className="flex items-center gap-2">
        <code className="flex-1 min-w-0 truncate text-xs font-mono px-3 py-2 rounded-lg bg-[var(--bg-surface)] border border-[var(--border-default)] text-[var(--text-secondary)]">
          {value}
        </code>
        <button
          onClick={handleCopy}
          className="shrink-0 text-xs px-2.5 py-2 rounded-lg bg-[var(--bg-elevated)] text-[var(--text-secondary)] hover:bg-[var(--bg-hover)] transition-colors"
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

async function dpFetchVisibility() {
  const r = await fetch('/api/apps/visibility')
  if (!r.ok) throw new Error('could not load app list')
  return r.json()
}

async function dpFetchDomain(appId) {
  const r = await fetch(`/api/apps/${encodeURIComponent(appId)}/domain`)
  if (r.status === 404) return null
  if (!r.ok) throw new Error('HTTP ' + r.status)
  return r.json()
}

async function dpAttachDomain(appId, domain) {
  const r = await fetch(`/api/apps/${encodeURIComponent(appId)}/domain`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ domain }),
  })
  const d = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status)
  return d
}

async function dpVerifyDomain(appId) {
  const r = await fetch(`/api/apps/${encodeURIComponent(appId)}/domain/verify`, { method: 'POST' })
  const d = await r.json().catch(() => ({}))
  if (!r.ok) {
    const err = new Error(d.error || 'HTTP ' + r.status)
    err.status = r.status
    throw err
  }
  return d
}

async function dpRemoveDomain(appId) {
  const r = await fetch(`/api/apps/${encodeURIComponent(appId)}/domain`, { method: 'DELETE' })
  const d = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(d.error || 'HTTP ' + r.status)
  return d
}

// ---------------------------------------------------------------------------
// DomainPanel
// ---------------------------------------------------------------------------

export default function DomainPanel() {
  const [apps, setApps] = useState(null) // null = loading, [] = none
  const [appsError, setAppsError] = useState('')
  const [selectedApp, setSelectedApp] = useState('')

  const [record, setRecord] = useState(null) // current CustomDomain, or null
  const [loadingRecord, setLoadingRecord] = useState(false)
  const [recordError, setRecordError] = useState('')

  const [domainInput, setDomainInput] = useState('')
  const [attaching, setAttaching] = useState(false)
  const [attachError, setAttachError] = useState('')

  const [verifying, setVerifying] = useState(false)
  const [verifyError, setVerifyError] = useState('')

  const [removing, setRemoving] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)

  // Load the app list once, restrict the picker to publicly-published apps
  // (attaching a domain to a private app is rejected by the backend).
  const loadApps = useCallback(() => {
    setAppsError('')
    dpFetchVisibility()
      .then(list => {
        const publicApps = (list || []).filter(a => a.visibility === 'public')
        setApps(publicApps)
        setSelectedApp(prev => prev || publicApps[0]?.app_id || '')
      })
      .catch(e => { setApps([]); setAppsError(e.message) })
  }, [])

  useEffect(() => { loadApps() }, [loadApps])

  // Load the domain record for the selected app.
  const loadRecord = useCallback(() => {
    if (!selectedApp) { setRecord(null); return }
    setLoadingRecord(true)
    setRecordError('')
    dpFetchDomain(selectedApp)
      .then(d => setRecord(d))
      .catch(e => setRecordError(e.message))
      .finally(() => setLoadingRecord(false))
  }, [selectedApp])

  useEffect(() => {
    setDomainInput('')
    setAttachError('')
    setVerifyError('')
    setConfirmRemove(false)
    loadRecord()
  }, [selectedApp, loadRecord])

  const handleAttach = async () => {
    const domain = domainInput.trim().toLowerCase()
    if (!domain) return
    setAttaching(true)
    setAttachError('')
    try {
      const d = await dpAttachDomain(selectedApp, domain)
      setRecord(d)
      setDomainInput('')
    } catch (e) {
      setAttachError(e.message)
    } finally {
      setAttaching(false)
    }
  }

  const handleVerify = async () => {
    setVerifying(true)
    setVerifyError('')
    try {
      const d = await dpVerifyDomain(selectedApp)
      setRecord(d)
    } catch (e) {
      // 409 body still carries {status:"pending", error} — surface it inline
      // rather than as a fatal error, since "not propagated yet" is expected.
      setVerifyError(e.message)
      if (e.status === 409) loadRecord()
    } finally {
      setVerifying(false)
    }
  }

  const handleRemove = async () => {
    setRemoving(true)
    try {
      await dpRemoveDomain(selectedApp)
      setRecord(null)
      setConfirmRemove(false)
    } catch (e) {
      setRecordError(e.message)
    } finally {
      setRemoving(false)
    }
  }

  return (
    <Section
      title="Custom Domain"
      desc="Point your own domain at a published app. Vulos verifies ownership with a DNS TXT record, then automatically provisions TLS."
    >
      {/* Loading apps */}
      {apps === null && !appsError && (
        <div className="flex items-center gap-2 py-8 justify-center text-[var(--text-muted)] text-xs">
          <span className="w-3.5 h-3.5 spinner" />
          Loading published apps...
        </div>
      )}

      {appsError && (
        <div className="p-3 rounded-lg bg-[var(--status-danger-soft)] border border-danger-soft mb-4">
          <p className="text-xs text-[var(--status-danger)]">{appsError}</p>
        </div>
      )}

      {/* Empty state — nothing published yet */}
      {apps !== null && apps.length === 0 && !appsError && (
        <div className="py-12 text-center">
          <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl border border-[var(--border-default)] text-[var(--text-faint)]">
            <svg viewBox="0 0 24 24" className="w-7 h-7" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="12" cy="12" r="9" /><path d="M2 12h20M12 3a15 15 0 0 1 0 18M12 3a15 15 0 0 0 0 18" />
            </svg>
          </div>
          <p className="text-sm text-[var(--text-secondary)]">No published apps yet.</p>
          <p className="text-xs text-[var(--text-faint)] mt-1.5 max-w-xs mx-auto leading-relaxed">
            Publish an app from the Dashboard's Web Publishing section first, then come back
            here to attach a custom domain to it.
          </p>
        </div>
      )}

      {/* Main flow — app selected */}
      {apps !== null && apps.length > 0 && (
        <>
          <Field label="App" hint="Only publicly-published apps can carry a custom domain.">
            <select value={selectedApp} onChange={e => setSelectedApp(e.target.value)} className="input">
              {apps.map(a => (
                <option key={a.app_id} value={a.app_id}>{a.app_id}</option>
              ))}
            </select>
          </Field>

          {loadingRecord && (
            <div className="flex items-center gap-2 py-6 justify-center text-[var(--text-muted)] text-xs">
              <span className="w-3.5 h-3.5 spinner" />
              Loading domain status...
            </div>
          )}

          {recordError && (
            <div className="p-3 rounded-lg bg-[var(--status-danger-soft)] border border-danger-soft mb-4">
              <p className="text-xs text-[var(--status-danger)]">{recordError}</p>
            </div>
          )}

          {/* No domain attached yet — attach form */}
          {!loadingRecord && !record && (
            <div className="mt-2 pt-4 border-t border-[var(--border-default)]">
              <Field label="Domain" hint="e.g. mysite.example.com — you must own it and control its DNS.">
                <input
                  value={domainInput}
                  onChange={e => setDomainInput(e.target.value)}
                  placeholder="mysite.example.com"
                  className="input"
                />
              </Field>
              <button
                onClick={handleAttach}
                disabled={attaching || !domainInput.trim()}
                className="btn text-sm disabled:opacity-50"
              >
                {attaching ? 'Attaching…' : 'Attach Domain'}
              </button>
              {attachError && (
                <div className="mt-3 text-xs rounded px-3 py-2 bg-[var(--status-danger-soft)] text-[var(--status-danger)]">
                  {attachError}
                </div>
              )}
            </div>
          )}

          {/* Domain attached — pending or verified */}
          {!loadingRecord && record && (
            <div className="mt-2 pt-4 border-t border-[var(--border-default)]">
              <div className="flex items-center gap-2 mb-4">
                <span className="text-sm font-medium text-[var(--text-primary)]">{record.domain}</span>
                <StatusBadge status={record.status} />
              </div>

              {record.status !== 'verified' && (
                <div className="mb-4 p-3 rounded-lg bg-[var(--bg-surface)] border border-[var(--border-default)]">
                  <p className="text-xs text-[var(--text-tertiary)] mb-3 leading-relaxed">
                    Create a DNS <strong className="text-[var(--text-secondary)]">TXT</strong> record at your
                    domain provider, then click Verify. Propagation can take a few minutes.
                  </p>
                  <CopyField label="TXT record name" value={record.txt_record} />
                  <CopyField label="TXT record value" value={record.challenge_token} />
                </div>
              )}

              <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-4">
                <InfoRow label="Status" value={record.status} />
                <InfoRow label="Created" value={record.created_at ? new Date(record.created_at).toLocaleString() : ''} />
                {record.verified_at && (
                  <InfoRow label="Verified" value={new Date(record.verified_at).toLocaleString()} />
                )}
              </div>

              <div className="flex gap-3 items-center flex-wrap">
                {record.status !== 'verified' && (
                  <button
                    onClick={handleVerify}
                    disabled={verifying}
                    className="btn text-sm disabled:opacity-50"
                  >
                    {verifying ? 'Verifying…' : 'Verify'}
                  </button>
                )}
                <button
                  onClick={loadRecord}
                  disabled={loadingRecord || verifying}
                  className="btn-ghost text-sm"
                >
                  Refresh
                </button>
                {!confirmRemove ? (
                  <button
                    onClick={() => setConfirmRemove(true)}
                    className="text-xs text-[var(--status-danger)] hover:text-[var(--status-danger)] ml-auto"
                  >
                    Remove domain
                  </button>
                ) : (
                  <span className="ml-auto flex items-center gap-2">
                    <span className="text-xs text-[var(--text-muted)]">Remove {record.domain}?</span>
                    <button
                      onClick={handleRemove}
                      disabled={removing}
                      className="text-xs px-2.5 py-1 rounded-lg bg-[var(--status-danger-soft)] text-[var(--status-danger)] hover:opacity-80 transition-opacity disabled:opacity-50"
                    >
                      {removing ? 'Removing…' : 'Confirm'}
                    </button>
                    <button
                      onClick={() => setConfirmRemove(false)}
                      disabled={removing}
                      className="text-xs text-[var(--text-muted)] hover:text-[var(--text-secondary)]"
                    >
                      Cancel
                    </button>
                  </span>
                )}
              </div>

              {verifyError && (
                <div className="mt-3 text-xs rounded px-3 py-2 bg-[var(--status-warning-soft)] text-[var(--status-warning)]">
                  Not verified yet: {verifyError}
                </div>
              )}
            </div>
          )}
        </>
      )}
    </Section>
  )
}
