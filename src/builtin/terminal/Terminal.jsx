import { useEffect, useRef, useState } from 'react'
import { Terminal as XTerm } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import 'xterm/css/xterm.css'

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/pty`

function SessionPicker({ sessions, onNew, onAttach, onKill }) {
  return (
    <div style={{
      display: 'flex', flexDirection: 'column', alignItems: 'center',
      justifyContent: 'center', height: '100%', background: '#0a0a0a',
      color: '#e5e5e5', fontFamily: "'SF Mono', 'Cascadia Code', monospace",
      fontSize: 14, gap: 16, padding: 24,
    }}>
      <div style={{ fontSize: 13, color: '#888', marginBottom: 4 }}>Terminal Sessions</div>
      {sessions.length > 0 && (
        <div style={{
          display: 'flex', flexDirection: 'column', gap: 8,
          width: '100%', maxWidth: 400,
        }}>
          {sessions.map(s => (
            <div key={s.id} style={{
              display: 'flex', alignItems: 'center', gap: 10,
              padding: '8px 12px', borderRadius: 6,
              background: '#1a1a1a', border: '1px solid #333',
            }}>
              <span style={{
                width: 8, height: 8, borderRadius: '50%',
                background: s.attached ? '#eab308' : s.alive ? '#22c55e' : '#666',
                flexShrink: 0,
              }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 13 }}>{s.id}</div>
                <div style={{ fontSize: 11, color: '#666' }}>
                  {s.attached ? 'attached elsewhere' : s.alive ? 'detached' : 'exited'}
                </div>
              </div>
              {s.alive && (
                <button onClick={() => onAttach(s.id)} style={{
                  background: '#333', border: 'none', color: '#e5e5e5',
                  padding: '4px 10px', borderRadius: 4, cursor: 'pointer', fontSize: 12,
                }}>
                  {s.attached ? 'Takeover' : 'Attach'}
                </button>
              )}
              <button onClick={() => onKill(s.id)} style={{
                background: 'transparent', border: 'none', color: '#666',
                padding: '4px 6px', cursor: 'pointer', fontSize: 12,
              }}>
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      <button onClick={onNew} style={{
        background: '#22c55e', border: 'none', color: '#0a0a0a',
        padding: '8px 20px', borderRadius: 6, cursor: 'pointer',
        fontSize: 13, fontWeight: 600, fontFamily: 'inherit',
      }}>
        New Session
      </button>
    </div>
  )
}

function TerminalView({ sessionID }) {
  const containerRef = useRef(null)

  useEffect(() => {
    if (!containerRef.current) return

    const term = new XTerm({
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: "'SF Mono', 'Cascadia Code', 'Fira Code', monospace",
      fontSize: 14,
      lineHeight: 1.2,
      theme: {
        background: '#0a0a0a',
        foreground: '#e5e5e5',
        cursor: '#e5e5e5',
        selectionBackground: '#333333',
        black: '#0a0a0a',
        red: '#ef4444',
        green: '#22c55e',
        yellow: '#eab308',
        blue: '#3b82f6',
        magenta: '#a855f7',
        cyan: '#06b6d4',
        white: '#e5e5e5',
        brightBlack: '#666666',
        brightRed: '#f87171',
        brightGreen: '#4ade80',
        brightYellow: '#facc15',
        brightBlue: '#60a5fa',
        brightMagenta: '#c084fc',
        brightCyan: '#22d3ee',
        brightWhite: '#ffffff',
      },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.loadAddon(new WebLinksAddon())

    term.open(containerRef.current)
    fitAddon.fit()

    const cols = term.cols
    const rows = term.rows
    let url = `${WS_URL}?cols=${cols}&rows=${rows}`
    if (sessionID) {
      url += `&session=${sessionID}`
    }
    const ws = new WebSocket(url)
    ws.binaryType = 'arraybuffer'

    ws.onopen = () => term.focus()

    ws.onmessage = (e) => {
      const data = typeof e.data === 'string' ? e.data : new TextDecoder().decode(e.data)
      term.write(data)
    }

    ws.onclose = () => {
      term.write('\r\n\x1b[90m[session ended]\x1b[0m\r\n')
    }

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })

    term.onResize(({ cols, rows }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new Uint8Array([1, ...new TextEncoder().encode(`${cols},${rows}`)]))
      }
    })

    const onResize = () => fitAddon.fit()
    window.addEventListener('resize', onResize)
    const resizeObserver = new ResizeObserver(() => fitAddon.fit())
    resizeObserver.observe(containerRef.current)

    return () => {
      window.removeEventListener('resize', onResize)
      resizeObserver.disconnect()
      ws.close()
      term.dispose()
    }
  }, [sessionID])

  return (
    <div
      ref={containerRef}
      className="w-full h-full"
      style={{ padding: '4px', background: '#0a0a0a' }}
    />
  )
}

export default function Terminal() {
  const [mode, setMode] = useState('loading') // loading | pick | terminal
  const [sessions, setSessions] = useState([])
  const [targetSession, setTargetSession] = useState(null)

  useEffect(() => {
    fetch('/api/pty/sessions')
      .then(r => r.json())
      .then(list => {
        if (!Array.isArray(list)) throw new Error('not array')
        const alive = list.filter(s => s.alive)
        if (alive.length === 0) {
          setMode('terminal')
        } else {
          setSessions(list)
          setMode('pick')
        }
      })
      .catch(() => {
        // No sessions or auth issue — just open new terminal
        setMode('terminal')
      })
  }, [])

  const handleKill = async (id) => {
    await fetch(`/api/pty/sessions?id=${id}`, { method: 'DELETE' })
    try {
      const res = await fetch('/api/pty/sessions')
      const list = await res.json()
      if (Array.isArray(list) && list.filter(s => s.alive).length > 0) {
        setSessions(list)
        return
      }
    } catch {}
    setMode('terminal')
  }

  if (mode === 'loading') {
    return <div style={{ background: '#0a0a0a', height: '100%' }} />
  }

  if (mode === 'pick') {
    return (
      <SessionPicker
        sessions={sessions}
        onNew={() => setMode('terminal')}
        onAttach={(id) => { setTargetSession(id); setMode('terminal') }}
        onKill={handleKill}
      />
    )
  }

  return <TerminalView sessionID={targetSession} />
}
