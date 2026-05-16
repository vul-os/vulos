import { useState } from 'react'

function Avatar({ name, size = 9 }) {
  const initials = (name || '?')
    .split(' ')
    .map(w => w[0])
    .join('')
    .toUpperCase()
    .slice(0, 2)
  const hue = [...(name || '')].reduce((h, c) => (h * 31 + c.charCodeAt(0)) & 0xffffff, 0)
  const bg = `hsl(${hue % 360}, 55%, 30%)`
  return (
    <div
      className={`w-${size} h-${size} rounded-full flex items-center justify-center text-sm font-semibold text-white shrink-0`}
      style={{ background: bg }}
    >
      {initials}
    </div>
  )
}

function RequestRow({ req, onApprove, onBlock }) {
  const [busy, setBusy] = useState(null) // 'approve' | 'block'

  const handle = async (action) => {
    setBusy(action)
    try {
      await (action === 'approve' ? onApprove(req.id) : onBlock(req.id))
    } finally {
      setBusy(null)
    }
  }

  const receivedAt = req.received_at
    ? new Date(req.received_at).toLocaleString()
    : null

  return (
    <div className="flex items-start gap-3 p-4 border border-neutral-800/50 rounded-xl hover:border-neutral-700/40 transition-colors">
      <Avatar name={req.display_name} />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 mb-0.5">
          <span className="text-sm font-medium text-neutral-100">
            {req.display_name || 'Unknown'}
          </span>
        </div>
        <p className="text-[11px] text-neutral-600 font-mono truncate mb-1">{req.vula_id}</p>
        {req.message && (
          <p className="text-xs text-neutral-400 italic mb-1">"{req.message}"</p>
        )}
        {req.server && (
          <p className="text-[11px] text-neutral-600">{req.server}</p>
        )}
        {receivedAt && (
          <p className="text-[11px] text-neutral-700 mt-0.5">{receivedAt}</p>
        )}
      </div>
      <div className="flex flex-col gap-1.5 shrink-0">
        <button
          onClick={() => handle('approve')}
          disabled={!!busy}
          className="px-3 py-1 text-xs font-medium rounded-lg bg-emerald-600/20 border border-emerald-500/30
            text-emerald-400 hover:bg-emerald-600/30 disabled:opacity-50 transition-colors"
        >
          {busy === 'approve' ? '…' : 'Approve'}
        </button>
        <button
          onClick={() => handle('block')}
          disabled={!!busy}
          className="px-3 py-1 text-xs font-medium rounded-lg bg-red-600/10 border border-red-500/20
            text-red-400 hover:bg-red-600/20 disabled:opacity-50 transition-colors"
        >
          {busy === 'block' ? '…' : 'Block'}
        </button>
      </div>
    </div>
  )
}

export default function RequestQueue({ requests, onApprove, onBlock }) {
  if (!requests || requests.length === 0) {
    return (
      <div className="text-center py-8 text-neutral-600 text-sm">
        No pending requests
      </div>
    )
  }

  return (
    <div className="space-y-2">
      {requests.map(req => (
        <RequestRow
          key={req.id}
          req={req}
          onApprove={onApprove}
          onBlock={onBlock}
        />
      ))}
    </div>
  )
}
