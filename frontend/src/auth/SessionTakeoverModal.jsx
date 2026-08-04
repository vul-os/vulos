import { useState } from 'react'
import { useAuth } from './AuthProvider'

// SESSION-TAKEOVER: shown right after a sign-in when the account is already
// active on another device. The user chooses to take over this device (sign the
// others out) or keep the other session (sign out here). Single-active-session
// UX; multi-session coexistence is a deliberately-unbuilt roadmap item
// (roadmap/MULTI-SESSION.md).
//
// Styled entirely from design tokens so it is correct in both light and dark,
// and responsive down to small phones (min() width, safe-area padding).
export default function SessionTakeoverModal() {
  const { sessionConflict, takeOverSession, keepOtherSessionAndSignOut } = useAuth()
  const [busy, setBusy] = useState(null) // 'take' | 'keep' | null

  if (!sessionConflict) return null

  const { others = 0, sessions = [] } = sessionConflict
  const otherSessions = sessions.filter((s) => !s.current)

  const deviceLabel = (s) => {
    if (!s) return 'Another device'
    if (s.provider && s.provider.startsWith('cloud')) return 'Cloud sign-in'
    if (s.device_id) return s.device_id
    return 'Another device'
  }
  const whenLabel = (iso) => {
    if (!iso) return ''
    try {
      const d = new Date(iso)
      if (Number.isNaN(d.getTime())) return ''
      return d.toLocaleString()
    } catch {
      return ''
    }
  }

  const onTakeOver = async () => {
    setBusy('take')
    try { await takeOverSession() } finally { setBusy(null) }
  }
  const onKeep = async () => {
    setBusy('keep')
    try { await keepOtherSessionAndSignOut() } finally { setBusy(null) }
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="session-takeover-title"
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 'var(--z-modal, 1000)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 'max(16px, env(safe-area-inset-top)) 16px max(16px, env(safe-area-inset-bottom))',
        background: 'var(--overlay, rgba(0,0,0,0.5))',
        backdropFilter: 'blur(var(--blur-overlay, 8px))',
      }}
    >
      <div
        style={{
          width: 'min(440px, 100%)',
          maxHeight: '90dvh',
          overflowY: 'auto',
          borderRadius: 'var(--radius-lg, 16px)',
          background: 'var(--surface-1, #17171a)',
          border: '1px solid var(--border, rgba(255,255,255,0.08))',
          boxShadow: 'var(--elevation-3, 0 24px 64px rgba(0,0,0,0.5))',
          padding: 'clamp(20px, 5vw, 28px)',
          color: 'var(--text-1, #f5f5f7)',
        }}
      >
        <h2
          id="session-takeover-title"
          style={{ margin: 0, fontSize: 'clamp(18px, 4.5vw, 20px)', fontWeight: 650, letterSpacing: '-0.01em' }}
        >
          You’re already signed in elsewhere
        </h2>
        <p style={{ marginTop: 10, marginBottom: 0, fontSize: 14, lineHeight: 1.5, color: 'var(--text-2, #b5b5bd)' }}>
          Your account is active on {others === 1 ? 'another device' : `${others} other devices`}. You can use
          Vulos here and sign the {others === 1 ? 'other one' : 'others'} out, or keep that session and stay signed
          out on this device.
        </p>

        {otherSessions.length > 0 && (
          <ul style={{ listStyle: 'none', margin: '16px 0 0', padding: 0, display: 'grid', gap: 8 }}>
            {otherSessions.slice(0, 4).map((s) => (
              <li
                key={s.id}
                style={{
                  display: 'flex',
                  justifyContent: 'space-between',
                  gap: 12,
                  fontSize: 13,
                  padding: '10px 12px',
                  borderRadius: 'var(--radius-md, 10px)',
                  background: 'var(--surface-2, rgba(255,255,255,0.04))',
                  border: '1px solid var(--border, rgba(255,255,255,0.06))',
                }}
              >
                <span style={{ fontWeight: 550 }}>{deviceLabel(s)}</span>
                <span style={{ color: 'var(--text-3, #85858d)' }}>{whenLabel(s.created_at)}</span>
              </li>
            ))}
          </ul>
        )}

        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            gap: 10,
            marginTop: 22,
          }}
        >
          <button
            type="button"
            onClick={onTakeOver}
            disabled={busy !== null}
            style={{
              width: '100%',
              minHeight: 44,
              borderRadius: 'var(--radius-md, 10px)',
              border: 'none',
              cursor: busy ? 'default' : 'pointer',
              fontSize: 15,
              fontWeight: 600,
              color: 'var(--accent-contrast, #fff)',
              background: 'var(--accent, #3b82f6)',
              opacity: busy && busy !== 'take' ? 0.6 : 1,
            }}
          >
            {busy === 'take' ? 'Signing out other devices…' : 'Use only here'}
          </button>
          <button
            type="button"
            onClick={onKeep}
            disabled={busy !== null}
            style={{
              width: '100%',
              minHeight: 44,
              borderRadius: 'var(--radius-md, 10px)',
              cursor: busy ? 'default' : 'pointer',
              fontSize: 15,
              fontWeight: 550,
              color: 'var(--text-1, #f5f5f7)',
              background: 'transparent',
              border: '1px solid var(--border-strong, rgba(255,255,255,0.16))',
              opacity: busy && busy !== 'keep' ? 0.6 : 1,
            }}
          >
            {busy === 'keep' ? 'Signing out…' : 'Keep my other session'}
          </button>
        </div>
      </div>
    </div>
  )
}
