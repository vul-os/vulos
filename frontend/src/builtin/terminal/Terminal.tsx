import { useEffect, useRef, useState, useCallback, type MouseEvent as ReactMouseEvent, type CSSProperties } from 'react'
import { Terminal as XTerm, type ITheme } from 'xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import 'xterm/css/xterm.css'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

const WS_URL = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/pty`

// ---------------------------------------------------------------------------
// Theme definitions
// Default theme is byte-identical to the original hardcoded values.
// ---------------------------------------------------------------------------
const THEMES: Record<string, ITheme> = {
  Default: {
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
  Solarized: {
    background: '#002b36',
    foreground: '#839496',
    cursor: '#839496',
    selectionBackground: '#073642',
    black: '#073642',
    red: '#dc322f',
    green: '#859900',
    yellow: '#b58900',
    blue: '#268bd2',
    magenta: '#d33682',
    cyan: '#2aa198',
    white: '#eee8d5',
    brightBlack: '#002b36',
    brightRed: '#cb4b16',
    brightGreen: '#586e75',
    brightYellow: '#657b83',
    brightBlue: '#839496',
    brightMagenta: '#6c71c4',
    brightCyan: '#93a1a1',
    brightWhite: '#fdf6e3',
  },
  Dracula: {
    background: '#282a36',
    foreground: '#f8f8f2',
    cursor: '#f8f8f2',
    selectionBackground: '#44475a',
    black: '#21222c',
    red: '#ff5555',
    green: '#50fa7b',
    yellow: '#f1fa8c',
    blue: '#bd93f9',
    magenta: '#ff79c6',
    cyan: '#8be9fd',
    white: '#f8f8f2',
    brightBlack: '#6272a4',
    brightRed: '#ff6e6e',
    brightGreen: '#69ff94',
    brightYellow: '#ffffa5',
    brightBlue: '#d6acff',
    brightMagenta: '#ff92df',
    brightCyan: '#a4ffff',
    brightWhite: '#ffffff',
  },
  Light: {
    background: '#fafafa',
    foreground: '#383a42',
    cursor: '#383a42',
    selectionBackground: '#d0d0d0',
    black: '#383a42',
    red: '#e45649',
    green: '#50a14f',
    yellow: '#c18401',
    blue: '#4078f2',
    magenta: '#a626a4',
    cyan: '#0184bc',
    white: '#fafafa',
    brightBlack: '#4f525e',
    brightRed: '#df6c75',
    brightGreen: '#98c379',
    brightYellow: '#e5c07b',
    brightBlue: '#61afef',
    brightMagenta: '#c678dd',
    brightCyan: '#56b6c2',
    brightWhite: '#ffffff',
  },
}

const THEME_NAMES = Object.keys(THEMES)

const FONT_FAMILIES = [
  "'SF Mono', 'Cascadia Code', 'Fira Code', monospace",
  "'Cascadia Code', monospace",
  "'Fira Code', monospace",
  "'JetBrains Mono', monospace",
  "'Courier New', monospace",
  "monospace",
]

const FONT_FAMILY_LABELS = [
  'SF Mono (default)',
  'Cascadia Code',
  'Fira Code',
  'JetBrains Mono',
  'Courier New',
  'System Mono',
]

const FONT_SIZES = [11, 12, 13, 14, 15, 16, 18, 20]

const STORAGE_KEY = 'terminal-prefs'

interface TerminalPrefs {
  theme: string
  fontFamily: string
  fontSize: number
}

const DEFAULT_PREFS: TerminalPrefs = {
  theme: 'Default',
  fontFamily: FONT_FAMILIES[0],
  fontSize: 14,
}

function loadPrefs(): TerminalPrefs {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return { ...DEFAULT_PREFS }
    const parsed: unknown = JSON.parse(raw)
    if (!isRecord(parsed)) return { ...DEFAULT_PREFS }
    const theme = typeof parsed.theme === 'string' && THEME_NAMES.includes(parsed.theme)
      ? parsed.theme
      : DEFAULT_PREFS.theme
    const fontFamily = typeof parsed.fontFamily === 'string' && FONT_FAMILIES.includes(parsed.fontFamily)
      ? parsed.fontFamily
      : DEFAULT_PREFS.fontFamily
    const fontSize = typeof parsed.fontSize === 'number' && FONT_SIZES.includes(parsed.fontSize)
      ? parsed.fontSize
      : DEFAULT_PREFS.fontSize
    return { theme, fontFamily, fontSize }
  } catch {
    return { ...DEFAULT_PREFS }
  }
}

function savePrefs(prefs: TerminalPrefs): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs))
  } catch { /* noop */ }
}

// A PTY session as returned by GET /api/pty/sessions.
interface PtySession {
  id: string
  alive?: boolean
  attached?: boolean
}

interface SessionPickerProps {
  sessions: PtySession[]
  onNew: () => void
  onAttach: (id: string) => void
  onKill: (id: string) => void
  prefs: TerminalPrefs
}

// ---------------------------------------------------------------------------
// Session Picker (unchanged behavior; uses prefs for colors)
// ---------------------------------------------------------------------------
function SessionPicker({ sessions, onNew, onAttach, onKill, prefs }: SessionPickerProps) {
  const theme = THEMES[prefs.theme]
  const hoverIn = (bg?: string) => (e: ReactMouseEvent<HTMLDivElement>) => { e.currentTarget.style.background = bg ?? '' }
  const hoverOut = (bg?: string) => (e: ReactMouseEvent<HTMLDivElement>) => { e.currentTarget.style.background = bg ?? '' }
  return (
    <div style={{
      display: 'flex', flexDirection: 'column', alignItems: 'center',
      justifyContent: 'center', height: '100%', background: theme.background,
      color: theme.foreground, fontFamily: prefs.fontFamily,
      fontSize: prefs.fontSize, gap: 20, padding: 24, boxSizing: 'border-box',
    }}>
      <div style={{ textAlign: 'center', display: 'flex', flexDirection: 'column', gap: 4 }}>
        <div style={{
          fontSize: 15, fontWeight: 600, color: theme.foreground,
          letterSpacing: '0.02em',
        }}>Terminal Sessions</div>
        <div style={{ fontSize: 11, color: theme.brightBlack }}>
          {sessions.length > 0
            ? `${sessions.length} session${sessions.length === 1 ? '' : 's'} available`
            : 'Start a new shell to begin'}
        </div>
      </div>
      {sessions.length > 0 && (
        <div style={{
          display: 'flex', flexDirection: 'column', gap: 8,
          width: '100%', maxWidth: 420,
        }}>
          {sessions.map(s => (
            <div key={s.id}
              onMouseEnter={hoverIn(theme.selectionBackground)}
              onMouseLeave={hoverOut(theme.black)}
              style={{
                display: 'flex', alignItems: 'center', gap: 11,
                padding: '10px 13px', borderRadius: 10,
                background: theme.black, border: `1px solid ${theme.selectionBackground}`,
                transition: 'background 0.15s ease',
              }}>
              <span style={{
                width: 9, height: 9, borderRadius: '50%',
                background: s.attached ? theme.yellow : s.alive ? theme.green : theme.brightBlack,
                boxShadow: s.alive ? `0 0 8px ${s.attached ? theme.yellow : theme.green}55` : 'none',
                flexShrink: 0,
              }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 13, fontWeight: 500, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.id}</div>
                <div style={{ fontSize: 11, color: theme.brightBlack, marginTop: 1 }}>
                  {s.attached ? 'attached elsewhere' : s.alive ? 'detached' : 'exited'}
                </div>
              </div>
              {s.alive && (
                <button onClick={() => onAttach(s.id)}
                  onMouseEnter={e => { e.currentTarget.style.background = theme.brightBlack ?? '' }}
                  onMouseLeave={e => { e.currentTarget.style.background = theme.selectionBackground ?? '' }}
                  style={{
                    background: theme.selectionBackground, border: 'none', color: theme.foreground,
                    padding: '5px 12px', borderRadius: 6, cursor: 'pointer', fontSize: 12,
                    fontWeight: 500, fontFamily: 'inherit', transition: 'background 0.15s ease',
                  }}>
                  {s.attached ? 'Takeover' : 'Attach'}
                </button>
              )}
              <button onClick={() => onKill(s.id)} aria-label={`Kill session ${s.id}`} title="Kill session"
                onMouseEnter={e => { e.currentTarget.style.color = theme.red ?? '' }}
                onMouseLeave={e => { e.currentTarget.style.color = theme.brightBlack ?? '' }}
                style={{
                  background: 'transparent', border: 'none', color: theme.brightBlack,
                  padding: '5px 7px', cursor: 'pointer', fontSize: 13, lineHeight: 1,
                  transition: 'color 0.15s ease',
                }}>
                ✕
              </button>
            </div>
          ))}
        </div>
      )}
      <button onClick={onNew}
        onMouseEnter={e => { e.currentTarget.style.filter = 'brightness(1.1)' }}
        onMouseLeave={e => { e.currentTarget.style.filter = 'none' }}
        style={{
          display: 'inline-flex', alignItems: 'center', gap: 8,
          background: theme.green, border: 'none', color: theme.background,
          padding: '9px 22px', borderRadius: 8, cursor: 'pointer',
          fontSize: 13, fontWeight: 600, fontFamily: 'inherit',
          transition: 'filter 0.15s ease',
        }}>
        <span style={{ fontSize: 16, lineHeight: 1, marginTop: -1 }}>+</span>
        New Session
      </button>
    </div>
  )
}

interface SettingsPanelProps {
  prefs: TerminalPrefs
  onChange: (prefs: TerminalPrefs) => void
  onClose: () => void
}

// ---------------------------------------------------------------------------
// Settings panel
// ---------------------------------------------------------------------------
function SettingsPanel({ prefs, onChange, onClose }: SettingsPanelProps) {
  const theme = THEMES[prefs.theme]
  const panelBg = theme.background === '#fafafa' ? '#f0f0f0' : '#1e1e1e'
  const panelFg = theme.foreground
  const borderColor = theme.selectionBackground

  const row: CSSProperties = { display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }
  const label: CSSProperties = { fontSize: 11, color: theme.brightBlack, minWidth: 72 }
  const select: CSSProperties = {
    flex: 1, background: theme.black, color: panelFg, border: `1px solid ${borderColor}`,
    borderRadius: 4, padding: '3px 6px', fontSize: 12, cursor: 'pointer',
    fontFamily: prefs.fontFamily,
  }

  return (
    <div style={{
      position: 'absolute', top: 32, right: 8, zIndex: 100,
      background: panelBg, border: `1px solid ${borderColor}`,
      borderRadius: 8, padding: '12px 14px', minWidth: 240,
      boxShadow: '0 4px 20px rgba(0,0,0,0.5)',
      fontFamily: prefs.fontFamily,
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <span style={{ fontSize: 12, fontWeight: 600, color: panelFg }}>Terminal Settings</span>
        <button onClick={onClose} style={{
          background: 'transparent', border: 'none', color: theme.brightBlack,
          cursor: 'pointer', fontSize: 14, lineHeight: 1, padding: 0,
        }}>✕</button>
      </div>

      {/* Theme */}
      <div style={row}>
        <span style={label}>Theme</span>
        <select
          style={select}
          value={prefs.theme}
          onChange={e => onChange({ ...prefs, theme: e.target.value })}
        >
          {THEME_NAMES.map(n => <option key={n} value={n}>{n}</option>)}
        </select>
      </div>

      {/* Font family */}
      <div style={row}>
        <span style={label}>Font</span>
        <select
          style={select}
          value={prefs.fontFamily}
          onChange={e => onChange({ ...prefs, fontFamily: e.target.value })}
        >
          {FONT_FAMILIES.map((f, i) => (
            <option key={f} value={f}>{FONT_FAMILY_LABELS[i]}</option>
          ))}
        </select>
      </div>

      {/* Font size */}
      <div style={row}>
        <span style={label}>Size</span>
        <select
          style={select}
          value={prefs.fontSize}
          onChange={e => onChange({ ...prefs, fontSize: Number(e.target.value) })}
        >
          {FONT_SIZES.map(s => <option key={s} value={s}>{s}px</option>)}
        </select>
      </div>

      {/* Preview swatch */}
      <div style={{
        marginTop: 4, padding: '6px 8px', borderRadius: 4,
        background: theme.background, border: `1px solid ${borderColor}`,
        fontSize: prefs.fontSize, fontFamily: prefs.fontFamily, color: theme.foreground,
        lineHeight: 1.4,
      }}>
        <span style={{ color: theme.green }}>user</span>
        <span style={{ color: theme.brightBlack }}>@host</span>
        <span style={{ color: theme.foreground }}>:~$ </span>
        <span style={{ color: theme.cyan }}>preview</span>
      </div>
    </div>
  )
}

interface TerminalViewProps {
  sessionID: string | null
  prefs: TerminalPrefs
}

// ---------------------------------------------------------------------------
// Terminal view
// ---------------------------------------------------------------------------
function TerminalView({ sessionID, prefs }: TerminalViewProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<XTerm | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const [showSettings, setShowSettings] = useState(false)
  const [currentPrefs, setCurrentPrefs] = useState<TerminalPrefs>(prefs)

  // Keep ref up-to-date for the resize observer closure
  const currentPrefsRef = useRef(currentPrefs)
  // eslint-disable-next-line react-hooks/refs
  currentPrefsRef.current = currentPrefs

  const handlePrefsChange = useCallback((newPrefs: TerminalPrefs) => {
    setCurrentPrefs(newPrefs)
    savePrefs(newPrefs)
    const term = termRef.current
    if (term) {
      term.options.theme = THEMES[newPrefs.theme]
      term.options.fontFamily = newPrefs.fontFamily
      term.options.fontSize = newPrefs.fontSize
      // Re-fit after font size change to recalculate cols/rows
      if (fitAddonRef.current) {
        requestAnimationFrame(() => fitAddonRef.current && fitAddonRef.current.fit())
      }
    }
    // Sync container background to new theme
    if (containerRef.current) {
      containerRef.current.style.background = THEMES[newPrefs.theme].background ?? ''
    }
  }, [])

  useEffect(() => {
    if (!containerRef.current) return

    const p = currentPrefsRef.current

    const term = new XTerm({
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: p.fontFamily,
      fontSize: p.fontSize,
      lineHeight: 1.2,
      theme: THEMES[p.theme],
    })

    termRef.current = term

    const fitAddon = new FitAddon()
    fitAddonRef.current = fitAddon
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

    ws.onmessage = (e: MessageEvent<string | ArrayBuffer>) => {
      const data = typeof e.data === 'string' ? e.data : new TextDecoder().decode(e.data)
      term.write(data)
    }

    ws.onclose = () => {
      term.write('\r\n\x1b[90m[session ended]\x1b[0m\r\n')
    }

    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })

    term.onResize(({ cols: c, rows: r }) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(new Uint8Array([1, ...new TextEncoder().encode(`${c},${r}`)]))
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
      termRef.current = null
      fitAddonRef.current = null
    }
  }, [sessionID])

  const theme = THEMES[currentPrefs.theme]

  return (
    <div style={{ position: 'relative', width: '100%', height: '100%' }}>
      {/* Gear button */}
      <button
        onClick={() => setShowSettings(v => !v)}
        title="Terminal settings"
        style={{
          position: 'absolute', top: 6, right: 8, zIndex: 101,
          background: showSettings ? theme.selectionBackground : 'transparent',
          border: `1px solid ${showSettings ? theme.selectionBackground : 'transparent'}`,
          color: theme.brightBlack,
          borderRadius: 4, width: 24, height: 24,
          cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 14, lineHeight: 1, transition: 'background 0.15s, color 0.15s',
        }}
        onMouseEnter={e => {
          e.currentTarget.style.background = theme.selectionBackground ?? ''
          e.currentTarget.style.color = theme.foreground ?? ''
          e.currentTarget.style.borderColor = theme.selectionBackground ?? ''
        }}
        onMouseLeave={e => {
          if (!showSettings) {
            e.currentTarget.style.background = 'transparent'
            e.currentTarget.style.color = theme.brightBlack ?? ''
            e.currentTarget.style.borderColor = 'transparent'
          }
        }}
      >
        ⚙
      </button>

      {/* Settings panel */}
      {showSettings && (
        <SettingsPanel
          prefs={currentPrefs}
          onChange={handlePrefsChange}
          onClose={() => setShowSettings(false)}
        />
      )}

      {/* xterm container */}
      <div
        ref={containerRef}
        className="w-full h-full"
        style={{ padding: '4px', background: theme.background }}
      />
    </div>
  )
}

type TerminalMode = 'loading' | 'pick' | 'terminal'

// isPtySessionArray narrows an unknown JSON response into PtySession[] — the
// only shape TerminalView/SessionPicker are allowed to see. A non-array or an
// element missing a string `id` is treated the same as "no sessions".
function isPtySessionArray(x: unknown): x is PtySession[] {
  return Array.isArray(x) && x.every(s => isRecord(s) && typeof s.id === 'string')
}

// ---------------------------------------------------------------------------
// Root Terminal component
// ---------------------------------------------------------------------------
export default function Terminal() {
  const [mode, setMode] = useState<TerminalMode>('loading')
  const [sessions, setSessions] = useState<PtySession[]>([])
  const [targetSession, setTargetSession] = useState<string | null>(null)
  const [prefs] = useState<TerminalPrefs>(loadPrefs)

  useEffect(() => {
    fetch('/api/pty/sessions')
      .then(r => r.json())
      .then((list: unknown) => {
        if (!isPtySessionArray(list)) throw new Error('not array')
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

  const handleKill = async (id: string) => {
    await fetch(`/api/pty/sessions?id=${id}`, { method: 'DELETE' })
    try {
      const res = await fetch('/api/pty/sessions')
      const list: unknown = await res.json()
      if (isPtySessionArray(list) && list.filter(s => s.alive).length > 0) {
        setSessions(list)
        return
      }
    } catch { /* noop */ }
    setMode('terminal')
  }

  if (mode === 'loading') {
    return <div style={{ background: THEMES[prefs.theme].background, height: '100%' }} />
  }

  if (mode === 'pick') {
    return (
      <SessionPicker
        sessions={sessions}
        onNew={() => setMode('terminal')}
        onAttach={(id) => { setTargetSession(id); setMode('terminal') }}
        onKill={handleKill}
        prefs={prefs}
      />
    )
  }

  return <TerminalView sessionID={targetSession} prefs={prefs} />
}
