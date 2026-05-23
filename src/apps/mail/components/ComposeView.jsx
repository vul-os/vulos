import { useState, useRef, useEffect } from 'react'

const inputStyle = {
  width: '100%',
  background: 'rgba(255,255,255,0.05)',
  border: '1px solid rgba(255,255,255,0.1)',
  borderRadius: 8,
  color: '#f5f5f5',
  fontSize: 12,
  padding: '7px 10px',
  outline: 'none',
  boxSizing: 'border-box',
  transition: 'border-color 0.15s',
}

const labelStyle = {
  fontSize: 10,
  color: 'rgba(255,255,255,0.35)',
  marginBottom: 4,
  display: 'block',
  fontWeight: 500,
  textTransform: 'uppercase',
  letterSpacing: '0.06em',
}

export default function ComposeView({ identity, onClose, onSent, prefillTo, prefillSubject, prefillBody }) {
  const [to, setTo] = useState(prefillTo || '')
  const [subject, setSubject] = useState(prefillSubject || '')
  const [body, setBody] = useState(prefillBody || '')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState(false)

  const toRef = useRef(null)

  useEffect(() => {
    // focus 'to' if empty, otherwise body
    if (!prefillTo) {
      toRef.current?.focus()
    }
  }, [prefillTo])

  const handleSend = async (e) => {
    e.preventDefault()
    setError('')
    if (!to.trim()) { setError('Recipient address is required.'); return }
    if (!subject.trim()) { setError('Subject is required.'); return }
    if (!body.trim()) { setError('Message body is required.'); return }

    setSending(true)
    try {
      const res = await fetch('/api/identity/send', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          to: to.trim(),
          subject: subject.trim(),
          body: body.trim(),
        }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error || `Failed to send (${res.status})`)
        setSending(false)
        return
      }
      setSuccess(true)
      setTimeout(() => {
        onSent?.()
        onClose?.()
      }, 900)
    } catch {
      setError('Network error — could not reach the mail service.')
      setSending(false)
    }
  }

  return (
    <div style={{
      display: 'flex',
      flexDirection: 'column',
      height: '100%',
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '9px 14px',
        borderBottom: '1px solid rgba(255,255,255,0.06)',
        flexShrink: 0,
      }}>
        <span style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.75)' }}>
          New Message
        </span>
        <button
          onClick={onClose}
          style={{
            background: 'transparent',
            border: 'none',
            color: 'rgba(255,255,255,0.3)',
            cursor: 'pointer',
            fontSize: 16,
            lineHeight: 1,
            padding: '2px 6px',
            borderRadius: 5,
          }}
          title="Discard draft"
          onMouseEnter={e => e.currentTarget.style.color = '#f87171'}
          onMouseLeave={e => e.currentTarget.style.color = 'rgba(255,255,255,0.3)'}
        >
          ✕
        </button>
      </div>

      {/* Form */}
      <form
        onSubmit={handleSend}
        style={{
          flex: 1,
          display: 'flex',
          flexDirection: 'column',
          gap: 10,
          padding: '12px 14px',
          overflowY: 'auto',
        }}
      >
        {identity?.address && (
          <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.3)', marginBottom: 2 }}>
            From: <span style={{ color: 'rgba(255,255,255,0.55)' }}>{identity.address}</span>
          </div>
        )}

        <div>
          <label style={labelStyle}>To</label>
          <input
            ref={toRef}
            style={inputStyle}
            value={to}
            onChange={e => setTo(e.target.value)}
            placeholder="user@vulos.org"
            type="text"
            autoComplete="off"
            spellCheck={false}
            onFocus={e => e.currentTarget.style.borderColor = 'rgba(99,102,241,0.5)'}
            onBlur={e => e.currentTarget.style.borderColor = 'rgba(255,255,255,0.1)'}
          />
        </div>

        <div>
          <label style={labelStyle}>Subject</label>
          <input
            style={inputStyle}
            value={subject}
            onChange={e => setSubject(e.target.value)}
            placeholder="(no subject)"
            type="text"
            onFocus={e => e.currentTarget.style.borderColor = 'rgba(99,102,241,0.5)'}
            onBlur={e => e.currentTarget.style.borderColor = 'rgba(255,255,255,0.1)'}
          />
        </div>

        <div style={{ flex: 1, display: 'flex', flexDirection: 'column' }}>
          <label style={labelStyle}>Message</label>
          <textarea
            style={{
              ...inputStyle,
              resize: 'none',
              flex: 1,
              minHeight: 120,
              fontFamily: 'inherit',
              lineHeight: 1.6,
            }}
            value={body}
            onChange={e => setBody(e.target.value)}
            placeholder="Write your message..."
            onFocus={e => e.currentTarget.style.borderColor = 'rgba(99,102,241,0.5)'}
            onBlur={e => e.currentTarget.style.borderColor = 'rgba(255,255,255,0.1)'}
          />
        </div>

        {error && (
          <div style={{
            fontSize: 11,
            color: '#f87171',
            padding: '7px 10px',
            background: 'rgba(239,68,68,0.08)',
            borderRadius: 7,
            border: '1px solid rgba(239,68,68,0.2)',
          }}>
            {error}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 4, flexShrink: 0 }}>
          <button
            type="button"
            onClick={onClose}
            style={{
              flex: 1,
              padding: '8px 0',
              borderRadius: 8,
              fontSize: 12,
              cursor: 'pointer',
              background: 'rgba(255,255,255,0.04)',
              border: '1px solid rgba(255,255,255,0.09)',
              color: 'rgba(255,255,255,0.45)',
            }}
          >
            Discard
          </button>
          <button
            type="submit"
            disabled={sending || success}
            style={{
              flex: 2,
              padding: '8px 0',
              borderRadius: 8,
              fontSize: 12,
              fontWeight: 600,
              cursor: (sending || success) ? 'default' : 'pointer',
              background: success
                ? 'rgba(34,197,94,0.25)'
                : sending
                  ? 'rgba(99,102,241,0.18)'
                  : 'rgba(99,102,241,0.45)',
              border: `1px solid ${success ? 'rgba(34,197,94,0.4)' : 'rgba(99,102,241,0.4)'}`,
              color: success ? '#86efac' : '#a5b4fc',
              transition: 'all 0.15s',
            }}
          >
            {success ? '✓ Sent' : sending ? 'Sending...' : 'Send →'}
          </button>
        </div>
      </form>
    </div>
  )
}
