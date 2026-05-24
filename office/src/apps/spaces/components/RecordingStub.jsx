/**
 * RecordingStub.jsx — Recording button (v1 stub — "Coming soon").
 *
 * Recording has serious privacy + compliance implications. This button is
 * intentionally non-functional in v1. See docs/MEET-RECORDING-PLAN.md for
 * the planned architecture (consent banner, encrypted upload, GDPR/POPIA).
 *
 * Props:
 *   none (stateless stub)
 */
import { Circle } from 'lucide-react'
import { Tooltip } from '../../../components/ui'

export default function RecordingStub() {
  return (
    <Tooltip
      label="Recording — coming soon. All participants will be notified before recording starts."
      side="top"
    >
      <button
        type="button"
        disabled
        aria-label="Recording (coming soon)"
        className={[
          'inline-flex items-center gap-1.5 h-10 px-3 rounded-md text-sm',
          'bg-paper/5 text-paper/35 cursor-not-allowed',
          'border border-paper/10',
          'transition-colors duration-fast',
        ].join(' ')}
      >
        <Circle size={12} className="text-danger/40" fill="currentColor" />
        <span className="tracking-tightish text-xs">Rec</span>
        <span
          className="inline-flex items-center px-1.5 py-0.5 rounded-pill text-[9px] font-semibold uppercase tracking-eyebrow bg-ink-faint/20 text-paper/40"
        >
          soon
        </span>
      </button>
    </Tooltip>
  )
}
