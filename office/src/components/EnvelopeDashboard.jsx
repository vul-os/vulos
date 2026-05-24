import { useEffect, useState, useCallback } from 'react'
import {
  FileSignature, Clock, CheckCircle2, XCircle, AlertCircle,
  RefreshCw, Bell, Trash2, ChevronDown, ChevronUp, Users,
} from 'lucide-react'
import { api } from '../lib/api.js'
import { Button, Card, IconButton, Tooltip } from './ui'

// OFFICE-45: Envelope Dashboard — per-envelope progress panel.
// Lists all signing envelopes with signer-level status, sequential/parallel
// mode badge, and action buttons: remind, cancel.
//
// Aesthetic direction — warm paper, single accent, warm signal hues
// (sage/honey/persimmon) for status; quiet horizontal progress bar; expandable
// per-signer rows with serif name + email.

const STATUS_META = {
  draft:     { label: 'Draft',     tone: 'neutral', icon: Clock },
  sent:      { label: 'Sent',      tone: 'info',    icon: Clock },
  completed: { label: 'Complete',  tone: 'success', icon: CheckCircle2 },
  declined:  { label: 'Declined',  tone: 'danger',  icon: XCircle },
  voided:    { label: 'Voided',    tone: 'warning', icon: AlertCircle },
}

const TONE_CLASSES = {
  neutral: 'bg-bg-elev2 text-ink-muted',
  info:    'bg-info-bg text-info',
  success: 'bg-success-bg text-success',
  danger:  'bg-danger-bg text-danger',
  warning: 'bg-warning-bg text-warning',
}

const SIGNER_STATUS_META = {
  pending:  { label: 'Pending',  dot: 'bg-line-strong' },
  sent:     { label: 'Sent',     dot: 'bg-info' },
  viewed:   { label: 'Viewed',   dot: 'bg-warning' },
  signed:   { label: 'Signed',   dot: 'bg-success' },
  declined: { label: 'Declined', dot: 'bg-danger' },
}

function StatusBadge({ status }) {
  const meta = STATUS_META[status] || STATUS_META.draft
  const Icon = meta.icon
  return (
    <span
      className={[
        'inline-flex items-center gap-1 px-2 py-0.5 rounded-pill text-2xs font-semibold tracking-tightish',
        TONE_CLASSES[meta.tone],
      ].join(' ')}
    >
      <Icon size={11} />
      {meta.label}
    </span>
  )
}

function ProgressBar({ value }) {
  const pct = Math.max(0, Math.min(100, Math.round(value)))
  return (
    <div className="w-full h-1 rounded-pill bg-bg-elev2 overflow-hidden">
      <div
        className="h-full bg-accent transition-[width] duration-slow ease-spring"
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

function SignerRow({ signer }) {
  const meta = SIGNER_STATUS_META[signer.status] || SIGNER_STATUS_META.pending
  return (
    <div className="flex items-center gap-2 py-1.5">
      <span className={`w-2 h-2 rounded-full flex-shrink-0 ${meta.dot}`} />
      <span className="font-serif italic text-sm text-ink flex-1 truncate">
        {signer.name || signer.email}
        {signer.email && signer.name && (
          <span className="ml-1.5 text-2xs text-ink-faint not-italic font-sans">
            {signer.email}
          </span>
        )}
      </span>
      <span className="text-2xs text-ink-faint tracking-tightish">
        #{signer.order}
      </span>
      <span className="text-2xs font-medium text-ink-muted tracking-tightish w-16 text-right">
        {meta.label}
      </span>
    </div>
  )
}

function EnvelopeRow({ envelope, onRemind, onCancel, onRefresh }) {
  const [expanded, setExpanded] = useState(false)
  const [status, setStatus] = useState(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null)

  const loadStatus = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await api.envelopeStatus(envelope.id)
      setStatus(data)
    } catch (e) {
      setError(e.message || 'Failed to load status')
    } finally {
      setLoading(false)
    }
  }, [envelope.id])

  useEffect(() => {
    if (expanded && !status) loadStatus()
  }, [expanded, status, loadStatus])

  const active = envelope.status !== 'completed'
    && envelope.status !== 'voided'
    && envelope.status !== 'declined'

  // Quiet per-envelope progress: count signed signers if status loaded;
  // else fall back to a heuristic from the envelope's own status.
  let progressPct = 0
  if (status?.signers?.length) {
    const total = status.signers.length
    const done = status.signers.filter(s => s.status === 'signed').length
    progressPct = (done / total) * 100
  } else if (envelope.status === 'completed') {
    progressPct = 100
  } else if (envelope.status === 'sent') {
    progressPct = 8 // a faint indication of "in flight"
  }

  return (
    <Card className="overflow-hidden transition-colors duration-fast ease-out">
      {/* Header row */}
      <div
        className="flex items-center gap-3 px-4 py-3 hover:bg-bg-elev2 cursor-pointer"
        onClick={() => setExpanded(e => !e)}
      >
        <FileSignature size={15} className="text-accent flex-shrink-0" />
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <span className="font-medium text-ink truncate text-sm tracking-tightish">
              {envelope.title}
            </span>
            <span
              className={[
                'text-2xs px-1.5 py-0.5 rounded-xs font-medium tracking-tightish',
                envelope.order_mode === 'sequential'
                  ? 'bg-accent-tint text-accent-press'
                  : 'bg-bg-elev2 text-ink-muted',
              ].join(' ')}
            >
              {envelope.order_mode === 'sequential' ? 'Sequential' : 'Parallel'}
            </span>
          </div>
          <div className="mt-2 max-w-xs">
            <ProgressBar value={progressPct} />
          </div>
        </div>

        <StatusBadge status={envelope.status} />

        <div className="flex items-center gap-0.5 ml-1" onClick={e => e.stopPropagation()}>
          {active && (
            <>
              <Tooltip label="Send reminders">
                <IconButton
                  size="sm"
                  onClick={() => onRemind(envelope.id)}
                  className="hover:bg-warning-bg hover:text-warning"
                >
                  <Bell size={13} />
                </IconButton>
              </Tooltip>
              <Tooltip label="Cancel envelope">
                <IconButton
                  size="sm"
                  onClick={() => onCancel(envelope.id)}
                  className="hover:bg-danger-bg hover:text-danger"
                >
                  <Trash2 size={13} />
                </IconButton>
              </Tooltip>
            </>
          )}
          <Tooltip label="Refresh">
            <IconButton
              size="sm"
              onClick={() => { loadStatus(); onRefresh() }}
            >
              <RefreshCw size={12} className={loading ? 'animate-spin' : ''} />
            </IconButton>
          </Tooltip>
          {expanded
            ? <ChevronUp size={14} className="text-ink-faint" />
            : <ChevronDown size={14} className="text-ink-faint" />}
        </div>
      </div>

      {/* Expanded signer list */}
      {expanded && (
        <div className="border-t border-line px-4 py-3 bg-bg-elev2 animate-fade-in">
          {loading && (
            <p className="text-2xs text-ink-faint py-1 font-serif italic">Loading…</p>
          )}
          {error && (
            <p className="text-2xs text-danger py-1">{error}</p>
          )}
          {status && (
            <div>
              <div className="flex items-center gap-1.5 mb-2 text-2xs text-ink-faint tracking-eyebrow uppercase font-semibold">
                <Users size={11} />
                <span>
                  {status.signers.length} signer{status.signers.length !== 1 ? 's' : ''}
                </span>
              </div>
              <div className="divide-y divide-line">
                {status.signers.map(sg => (
                  <SignerRow key={sg.id} signer={sg} />
                ))}
              </div>
            </div>
          )}
          {!loading && !status && !error && (
            <p className="text-2xs text-ink-faint font-serif italic">
              No signer data — click refresh.
            </p>
          )}
        </div>
      )}
    </Card>
  )
}

export default function EnvelopeDashboard() {
  const [envelopes, setEnvelopes] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [toast, setToast] = useState(null)

  const showToast = useCallback((msg, type = 'info') => {
    setToast({ msg, type })
    setTimeout(() => setToast(null), 3500)
  }, [])

  const loadEnvelopes = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await api.listEnvelopes()
      setEnvelopes(Array.isArray(data) ? data : [])
    } catch (e) {
      setError(e.message || 'Failed to load envelopes')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadEnvelopes() }, [loadEnvelopes])

  const handleRemind = useCallback(async (envelopeId) => {
    try {
      const res = await api.envelopeRemind(envelopeId)
      const reminded = res.reminded || []
      showToast(`Reminders sent to ${reminded.length} signer(s).`, 'success')
    } catch (e) {
      showToast(e.message || 'Failed to send reminders', 'error')
    }
  }, [showToast])

  const handleCancel = useCallback(async (envelopeId) => {
    if (!window.confirm('Cancel (void) this envelope? This cannot be undone.')) return
    try {
      await api.envelopeCancel(envelopeId)
      showToast('Envelope voided.', 'success')
      loadEnvelopes()
    } catch (e) {
      showToast(e.message || 'Failed to cancel envelope', 'error')
    }
  }, [showToast, loadEnvelopes])

  return (
    <div className="max-w-3xl mx-auto py-8 px-4">
      {/* Toast — quiet, paper-on-ink */}
      {toast && (
        <div
          className={[
            'fixed top-4 right-4 z-50 px-4 py-2 rounded-md shadow-e2 text-xs font-medium tracking-tightish animate-fade-in',
            toast.type === 'success' ? 'bg-success text-white'
              : toast.type === 'error' ? 'bg-danger text-white'
              : 'bg-ink text-paper',
          ].join(' ')}
        >
          {toast.msg}
        </div>
      )}

      <header className="mb-6">
        <p className="text-2xs font-semibold text-ink-faint tracking-eyebrow uppercase mb-1">
          Signing
        </p>
        <div className="flex items-center justify-between gap-3">
          <h1 className="font-serif text-2xl text-ink tracking-tightish flex items-center gap-2.5">
            Envelopes
          </h1>
          <Button
            variant="secondary"
            size="sm"
            onClick={loadEnvelopes}
          >
            <RefreshCw size={13} className={loading ? 'animate-spin' : ''} />
            Refresh
          </Button>
        </div>
      </header>

      {loading && (
        <div className="flex justify-center py-12">
          <div className="w-6 h-6 border-2 border-accent border-t-transparent rounded-full animate-spin" />
        </div>
      )}

      {error && (
        <div className="text-sm text-danger bg-danger-bg border border-line rounded-md px-4 py-3 flex items-center gap-2">
          <AlertCircle size={14} className="shrink-0" />
          {error}
        </div>
      )}

      {!loading && !error && envelopes.length === 0 && (
        <div className="text-center py-20">
          <FileSignature size={36} className="mx-auto mb-3 text-ink-faint opacity-50" />
          <p className="font-serif italic text-ink-muted">
            No signing envelopes yet.
          </p>
        </div>
      )}

      {!loading && !error && envelopes.length > 0 && (
        <div className="space-y-2">
          {envelopes.map(env => (
            <EnvelopeRow
              key={env.id}
              envelope={env}
              onRemind={handleRemind}
              onCancel={handleCancel}
              onRefresh={loadEnvelopes}
            />
          ))}
        </div>
      )}
    </div>
  )
}
