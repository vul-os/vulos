// no-broker-dep:allow-file: comment describes ICE servers coming from the relayconfig provider
// seam using the same legacy 'ephor by default' shorthand as
// backend/services/peering/ice.go -- no import, no dependency;
// stale-terminology finding reported separately, not fixed here.

import {
  useState,
  useEffect,
  useRef,
  useCallback,
  type RefObject,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type WheelEvent as ReactWheelEvent,
} from 'react'
import { applyLowLatencyHints } from './lowLatency'

// RTCRtpReceiver.playoutDelayHint is a real, shipping Chrome API (see
// lowLatency.ts) but is absent from TypeScript's lib.dom.d.ts, so the real
// RTCRtpTransceiver returned by pc.addTransceiver() has zero declared
// properties in common with lowLatency's LowLatencyReceiver — TS's weak-type
// detection then rejects passing it in, even though at runtime the object is
// exactly what LowLatencyTransceiver expects. Augmenting the ambient DOM type
// (rather than casting the value) fixes this honestly: it is a compile-time
// acknowledgment of a field Chrome already has, not a change to any object
// at runtime.
declare global {
  interface RTCRtpReceiver {
    playoutDelayHint?: number
  }
}

// Cloud-gaming grade stream viewer — connects via WebRTC with split input channels.
// Mouse: unreliable/unordered (latest-wins, high freq)
// Keyboard: reliable/ordered (every event must arrive in order)
// Gamepad: unreliable/unordered (full state snapshots)

// Modifier bitmask — sent with every keyboard event for state recovery
const MOD_SHIFT    = 1
const MOD_CTRL     = 2
const MOD_ALT      = 4
const MOD_META     = 8
const MOD_CAPSLOCK = 16

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

// ── Wire shapes (untyped JSON in, narrowed before use) ─────────────────────────

// Mirrors backend/services/stream/stream.go Session (only the fields this
// viewer actually reads).
interface StreamSession {
  id: string
  running: boolean
  width?: number
  height?: number
  quality?: string
}

function toStreamSession(x: unknown): StreamSession | null {
  if (!isRecord(x) || typeof x.id !== 'string' || typeof x.running !== 'boolean') return null
  return {
    id: x.id,
    running: x.running,
    width: typeof x.width === 'number' ? x.width : undefined,
    height: typeof x.height === 'number' ? x.height : undefined,
    quality: typeof x.quality === 'string' ? x.quality : undefined,
  }
}

function toStreamSessions(x: unknown): StreamSession[] {
  return Array.isArray(x) ? x.map(toStreamSession).filter((s): s is StreamSession => s !== null) : []
}

// Mirrors backend/services/peering/ice.go iceServer/iceConfigResponse.
function toIceServer(x: unknown): RTCIceServer | null {
  if (!isRecord(x) || !Array.isArray(x.urls)) return null
  const urls = x.urls.filter((u): u is string => typeof u === 'string')
  if (urls.length === 0) return null
  const server: RTCIceServer = { urls }
  if (typeof x.username === 'string') server.username = x.username
  if (typeof x.credential === 'string') server.credential = x.credential
  return server
}

function toIceServers(x: unknown): RTCIceServer[] {
  if (!isRecord(x) || !Array.isArray(x.ice_servers)) return []
  return x.ice_servers.map(toIceServer).filter((s): s is RTCIceServer => s !== null)
}

function toIceCandidateInit(x: unknown): RTCIceCandidateInit | null {
  if (!isRecord(x)) return null
  const init: RTCIceCandidateInit = {}
  if (typeof x.candidate === 'string') init.candidate = x.candidate
  if (typeof x.sdpMid === 'string' || x.sdpMid === null) init.sdpMid = x.sdpMid
  if (typeof x.sdpMLineIndex === 'number' || x.sdpMLineIndex === null) init.sdpMLineIndex = x.sdpMLineIndex
  if (typeof x.usernameFragment === 'string' || x.usernameFragment === null) init.usernameFragment = x.usernameFragment
  return init
}

// Outbound input-event shapes — mirror the anonymous structs decoded by
// backend/services/stream/stream.go's handleMouse/handleKeyboard.
interface StreamMouseEvent {
  t: 'mm' | 'mr' | 'md' | 'mu' | 'sc'
  x?: number
  y?: number
  dx?: number
  dy?: number
  b?: number
}

interface StreamKbdEvent {
  t: 'kd' | 'ku'
  key: string
  code: string
  mod: number
}

// ---------------------------------------------------------------------------
// StreamToolbar — gamer overlay: FPS selector, live RTT/quality, fullscreen,
// MangoHud toggle.  All identifiers are gameToolbar-/streamToolbar- prefixed
// to avoid collisions with existing component state.
// ---------------------------------------------------------------------------

const STREAM_TOOLBAR_FPS_OPTIONS = [30, 60, 90, 120]

// How many 1s polls to wait for a session to move to running before admitting
// it is not coming. ~20s is longer than a cold app launch on a slow box and
// far shorter than "forever", which is what this used to be.
const MAX_START_ATTEMPTS = 20

interface StreamToolbarProps {
  sessionId: string
  pcRef: RefObject<RTCPeerConnection | null>
  quality: string | null
  streamToolbarCollapsed: boolean
  onStreamToolbarToggle: () => void
}

function StreamToolbar({
  sessionId,
  pcRef,
  quality,
  streamToolbarCollapsed,
  onStreamToolbarToggle,
}: StreamToolbarProps) {
  const [streamToolbarFps, setStreamToolbarFps] = useState(30)
  const [streamToolbarRtt, setStreamToolbarRtt] = useState<number | null>(null)
  const [streamToolbarMangoHud, setStreamToolbarMangoHud] = useState(false)
  const [streamToolbarFullscreen, setStreamToolbarFullscreen] = useState(false)
  const streamToolbarRttRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // Poll ICE candidate-pair RTT from WebRTC stats every 2 s
  useEffect(() => {
    streamToolbarRttRef.current = setInterval(async () => {
      const pc = pcRef.current
      if (!pc) return
      try {
        const stats = await pc.getStats()
        stats.forEach((report: unknown) => {
          if (
            isRecord(report) &&
            report.type === 'candidate-pair' &&
            report.state === 'succeeded' &&
            report.nominated &&
            typeof report.currentRoundTripTime === 'number'
          ) {
            setStreamToolbarRtt(Math.round(report.currentRoundTripTime * 1000))
          }
        })
      } catch {
        // getStats can throw if PC is closing — ignore
      }
    }, 2000)
    return () => {
      if (streamToolbarRttRef.current) clearInterval(streamToolbarRttRef.current)
    }
  }, [pcRef])

  // Track fullscreen state from browser events
  useEffect(() => {
    const onFsChange = () => setStreamToolbarFullscreen(!!document.fullscreenElement)
    document.addEventListener('fullscreenchange', onFsChange)
    return () => document.removeEventListener('fullscreenchange', onFsChange)
  }, [])

  const streamToolbarSetFps = useCallback(async (fps: number) => {
    setStreamToolbarFps(fps)
    try {
      await fetch('/api/stream/fps', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: sessionId, fps }),
      })
    } catch {
      // non-fatal — toolbar still shows selection
    }
  }, [sessionId])

  const streamToolbarToggleMangoHud = useCallback(async () => {
    const next = !streamToolbarMangoHud
    setStreamToolbarMangoHud(next)
    try {
      await fetch('/api/stream/mangohud', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: sessionId, enabled: next }),
      })
    } catch {
      // non-fatal
    }
  }, [sessionId, streamToolbarMangoHud])

  const streamToolbarToggleFullscreen = useCallback(async () => {
    if (!document.fullscreenElement) {
      const el = document.documentElement
      try {
        await el.requestFullscreen()
        // Request pointer lock so mouse is captured inside the stream
        el.requestPointerLock?.()
      } catch {
        // Browser may deny fullscreen without a transient user gesture — ignore
      }
    } else {
      try {
        await document.exitFullscreen()
        document.exitPointerLock?.()
      } catch {
        // ignore
      }
    }
  }, [])

  const streamToolbarRttColor =
    streamToolbarRtt == null ? 'text-white/40' :
    streamToolbarRtt < 50   ? 'text-success' :
    streamToolbarRtt < 120  ? 'text-warning' :
                               'text-danger'

  const streamToolbarQualityColor =
    quality === 'max'    ? 'text-success' :
    quality === 'high'   ? 'text-success' :
    quality === 'medium' ? 'text-warning' :
    quality === 'low'    ? 'text-danger' :
                           'text-white/40'

  if (streamToolbarCollapsed) {
    return (
      <button
        className="absolute top-3 right-3 z-30 inline-flex items-center justify-center min-w-[40px] min-h-[40px] rounded-xl bg-black/50 text-white/70 text-sm backdrop-blur-md ring-1 ring-white/10 shadow-lg shadow-black/30 hover:bg-black/70 hover:text-white transition-[background-color,color] duration-[var(--motion-fast)] ease-[var(--ease-out)] select-none focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        onMouseDown={e => e.stopPropagation()}
        onClick={onStreamToolbarToggle}
        title="Show stream toolbar"
        aria-label="Show stream toolbar"
      >
        <span aria-hidden="true">⚙</span>
      </button>
    )
  }

  return (
    <div
      className="stream-fade-in absolute top-0 left-0 right-0 z-30 flex items-center gap-2 sm:gap-3 px-2.5 sm:px-3 py-2 bg-gradient-to-b from-black/85 to-black/40 backdrop-blur-md text-xs select-none border-b border-white/5 overflow-x-auto min-w-0"
      onMouseDown={e => e.stopPropagation()}
      onKeyDown={e => e.stopPropagation()}
    >
      {/* Live indicator */}
      <span
        className="stream-pulse shrink-0 w-2 h-2 rounded-full bg-[var(--status-success)] shadow-[0_0_8px_var(--status-success)] motion-reduce:animate-none"
        aria-hidden="true"
      />

      {/* FPS selector */}
      <div className="flex items-center gap-1 shrink-0">
        <span className="text-white/40 mr-0.5 tracking-wide">FPS</span>
        {STREAM_TOOLBAR_FPS_OPTIONS.map(fps => (
          <button
            key={fps}
            onClick={() => streamToolbarSetFps(fps)}
            aria-pressed={streamToolbarFps === fps}
            aria-label={`${fps} frames per second`}
            className={`px-2 py-1 min-w-[34px] text-center rounded-md tabular-nums transition-[background-color,color] duration-[var(--motion-fast)] ease-[var(--ease-out)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] ${
              streamToolbarFps === fps
                ? 'bg-[var(--accent)] text-white shadow-sm shadow-black/30'
                : 'bg-white/5 text-white/60 hover:bg-white/10 hover:text-white'
            }`}
          >
            {fps}
          </button>
        ))}
      </div>

      <div className="shrink-0 w-px h-4 bg-white/10" />

      {/* Live RTT */}
      <div
        className={`flex items-center gap-1 shrink-0 tabular-nums ${streamToolbarRttColor}`}
        aria-label={`Round-trip latency ${streamToolbarRtt != null ? `${streamToolbarRtt} milliseconds` : 'unknown'}`}
      >
        <span className="text-white/40 tracking-wide" aria-hidden="true">RTT</span>
        <span>{streamToolbarRtt != null ? `${streamToolbarRtt}ms` : '—'}</span>
      </div>

      <div className="shrink-0 w-px h-4 bg-white/10" />

      {/* Quality tier */}
      <div
        className={`flex items-center gap-1 shrink-0 capitalize ${streamToolbarQualityColor}`}
        aria-label={`Stream quality ${quality || 'unknown'}`}
      >
        <span className="text-white/40 tracking-wide" aria-hidden="true">Q</span>
        <span>{quality || '—'}</span>
      </div>

      <div className="shrink-0 w-px h-4 bg-white/10" />

      {/* MangoHud toggle */}
      <button
        onClick={streamToolbarToggleMangoHud}
        aria-pressed={streamToolbarMangoHud}
        className={`shrink-0 px-2 py-1 rounded-md font-medium tracking-wide transition-[background-color,color] duration-[var(--motion-fast)] ease-[var(--ease-out)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)] ${
          streamToolbarMangoHud
            ? 'bg-orange-500 text-white shadow-sm shadow-black/30'
            : 'bg-white/5 text-white/60 hover:bg-white/10 hover:text-white'
        }`}
        title="Toggle MangoHud overlay (restarts capture)"
        aria-label="Toggle MangoHud performance overlay"
      >
        HUD
      </button>

      <div className="shrink-0 w-px h-4 bg-white/10" />

      {/* Fullscreen + pointer-lock */}
      <button
        onClick={streamToolbarToggleFullscreen}
        className="shrink-0 px-2 py-1 rounded-md bg-white/5 text-white/60 hover:bg-white/10 hover:text-white transition-[background-color,color] duration-[var(--motion-fast)] ease-[var(--ease-out)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        title={streamToolbarFullscreen ? 'Exit fullscreen' : 'Fullscreen + pointer lock (Esc to exit)'}
        aria-label={streamToolbarFullscreen ? 'Exit fullscreen' : 'Enter fullscreen with pointer lock'}
      >
        <span aria-hidden="true">{streamToolbarFullscreen ? '⤓' : '⤢'}</span>
      </button>

      {/* Collapse button */}
      <button
        onClick={onStreamToolbarToggle}
        className="shrink-0 ml-auto px-2 py-1 rounded-md bg-white/5 text-white/50 hover:bg-white/10 hover:text-white transition-[background-color,color] duration-[var(--motion-fast)] ease-[var(--ease-out)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
        title="Hide toolbar"
        aria-label="Hide stream toolbar"
      >
        <span aria-hidden="true">✕</span>
      </button>
    </div>
  )
}

function getModifiers(e: ReactKeyboardEvent): number {
  let m = 0
  if (e.shiftKey) m |= MOD_SHIFT
  if (e.ctrlKey)  m |= MOD_CTRL
  if (e.altKey)   m |= MOD_ALT
  if (e.metaKey)  m |= MOD_META
  if (e.getModifierState?.('CapsLock')) m |= MOD_CAPSLOCK
  return m
}

interface StreamViewerProps {
  sessionId: string
  scrollSensitivity?: number
  gaming?: boolean
}

export default function StreamViewer({ sessionId, scrollSensitivity = 1.0, gaming = false }: StreamViewerProps) {
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const pcRef = useRef<RTCPeerConnection | null>(null)
  const mouseDcRef = useRef<RTCDataChannel | null>(null)
  const kbdDcRef = useRef<RTCDataChannel | null>(null)
  const gpDcRef = useRef<RTCDataChannel | null>(null)
  const gpLoopRef = useRef<number | null>(null)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const connectedRef = useRef(false)
  const lastMouseRef = useRef(0)
  const scrollAccRef = useRef(0)
  const scrollRafRef = useRef<number | null>(null)
  const pointerLockedRef = useRef(false)
  // 'starting'   — the session exists on the box but is not running yet, so we
  //                are genuinely waiting for something that is expected to
  //                arrive. This is the ONLY state entitled to a spinner.
  // 'connecting' — session is running; WebRTC/signalling is negotiating.
  // 'connected'  — media is flowing.
  // A failure is NOT a status; it lives in `error` and renders a distinct
  // surface. See the STREAM-01 note on the error branch below.
  const [status, setStatus] = useState<'starting' | 'connecting' | 'connected'>('starting')
  const [error, setError] = useState<string | null>(null)
  // Bounds the "waiting for the session to start" poll. Without a bound a
  // session that never starts spins forever with no way for the user to tell
  // that nothing is going to happen (STREAM-02).
  const startAttemptsRef = useRef(0)
  // Set once the viewer has stopped retrying by itself. It gates the automatic
  // reconnect below so a verdict ("this never started") stays on screen instead
  // of flickering against a spinner every 2s; the user re-arms it with Retry.
  const [gaveUp, setGaveUp] = useState(false)
  // Every pending reconnect/poll timer, so unmount can cancel them. A stream
  // window closed while its session was not running used to leave a 1s loop
  // building a fresh RTCPeerConnection + WebSocket forever (STREAM-03).
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelledRef = useRef(false)
  const [pointerLocked, setPointerLocked] = useState(false)
  const streamSize = useRef({ w: 1280, h: 720 })

  // Stream toolbar state — GAME-08
  const [streamToolbarQuality, setStreamToolbarQuality] = useState<string | null>(null)
  const [streamToolbarCollapsed, setStreamToolbarCollapsed] = useState(false)

  const sendMouse = useCallback((evt: StreamMouseEvent) => {
    const dc = mouseDcRef.current
    if (!dc || dc.readyState !== 'open') return
    dc.send(JSON.stringify(evt))
  }, [])

  const sendKbd = useCallback((evt: StreamKbdEvent) => {
    const dc = kbdDcRef.current
    if (!dc || dc.readyState !== 'open') return
    dc.send(JSON.stringify(evt))
  }, [])

  const connect = useCallback(async () => {
    if (cancelledRef.current) return
    pcRef.current?.close()
    wsRef.current?.close()
    connectedRef.current = false
    setError(null)

    try {
      const res = await fetch('/api/stream/sessions')
      // A 5xx/404 from the box used to be fed straight into toStreamSessions(),
      // which answers [] for any shape it doesn't recognise — so a dead backend
      // was indistinguishable from "your session isn't up yet" and the viewer
      // sat in the start-poll forever.
      if (!res.ok) throw new Error(`Box returned HTTP ${res.status} for the session list`)
      const sessions = toStreamSessions(await res.json())
      if (cancelledRef.current) return
      const session = sessions.find(s => s.id === sessionId)
      if (!session || !session.running) {
        startAttemptsRef.current += 1
        if (startAttemptsRef.current > MAX_START_ATTEMPTS) {
          setGaveUp(true)
          setError(
            sessions.length === 0
              ? 'The box reports no running stream sessions.'
              : 'This session is registered on the box but never started.',
          )
          return
        }
        setStatus('starting')
        retryTimerRef.current = setTimeout(() => connect(), 1000)
        return
      }
      startAttemptsRef.current = 0
      setStatus('connecting')
      streamSize.current = { w: session.width || 1280, h: session.height || 720 }

      // RELAY-01: ICE servers come from the box's single relay/TURN provider
      // seam (ephor by default; BYO turn/libp2p/wireguard/none otherwise —
      // see Settings > Network > Relay & Reachability) instead of a
      // hardcoded public STUN server, so a self-hoster's choice there
      // actually applies here too. A transient fetch failure keeps
      // iceServers EMPTY rather than leaking to public Google STUN, which
      // would silently defeat VULOS_STUN_DISABLE_PUBLIC.
      let iceServers: RTCIceServer[] = []
      try {
        const iceRes = await fetch('/api/peering/ice')
        if (iceRes.ok) {
          const servers = toIceServers(await iceRes.json())
          if (servers.length) iceServers = servers
        }
      } catch {
        // keep iceServers empty — never fall back to third-party STUN
      }

      const pc = new RTCPeerConnection({ iceServers })
      pcRef.current = pc

      pc.ontrack = (e) => {
        if (videoRef.current && e.streams[0]) {
          videoRef.current.srcObject = e.streams[0]
          connectedRef.current = true
          setStatus('connected')
        }
      }

      pc.oniceconnectionstatechange = () => {
        const state = pc.iceConnectionState
        if (state === 'failed') setError('Connection failed')
        else if (state === 'disconnected') {
          setTimeout(() => {
            if (pc.iceConnectionState === 'disconnected') setError('Connection lost')
          }, 5000)
        }
      }

      // Mouse channel — unreliable, unordered (latest-wins for position)
      const mouseDc = pc.createDataChannel('mouse', { ordered: false, maxRetransmits: 0 })
      mouseDcRef.current = mouseDc

      // Keyboard channel — reliable, ordered (every key must arrive in sequence)
      const kbdDc = pc.createDataChannel('keyboard', { ordered: true })
      kbdDcRef.current = kbdDc

      // Gamepad channel — unreliable, unordered (full state snapshots, latest-wins)
      const gpDc = pc.createDataChannel('gamepad', { ordered: false, maxRetransmits: 0 })
      gpDcRef.current = gpDc

      const videoTransceiver = pc.addTransceiver('video', { direction: 'recvonly' })
      // Gaming sessions: minimise the receive-side jitter buffer so the browser
      // doesn't add playout latency. Non-gaming keeps the default (stability).
      applyLowLatencyHints(videoTransceiver, gaming)

      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/api/stream/ws?id=${sessionId}`)
      wsRef.current = ws

      ws.onmessage = (e) => {
        if (!e.data || typeof e.data !== 'string') return
        let msg: unknown
        try { msg = JSON.parse(e.data) } catch { return }
        if (!isRecord(msg)) return
        if (msg.type === 'answer' && typeof msg.sdp === 'string') {
          pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }))
        } else if (msg.type === 'candidate' && msg.candidate) {
          const init = toIceCandidateInit(msg.candidate)
          if (init) pc.addIceCandidate(new RTCIceCandidate(init))
        }
      }

      ws.onopen = async () => {
        const offer = await pc.createOffer()
        await pc.setLocalDescription(offer)
        pc.onicecandidate = (e) => {
          if (e.candidate) ws.send(JSON.stringify({ type: 'candidate', candidate: e.candidate.toJSON() }))
        }
        ws.send(JSON.stringify({ type: 'offer', sdp: offer.sdp }))
      }

      ws.onerror = () => { if (!connectedRef.current) setError('WebSocket connection failed') }
      ws.onclose = () => { if (!connectedRef.current) setError('Signaling connection lost') }
    } catch (err) {
      setError(errorMessage(err))
    }
  }, [sessionId, gaming])

  useEffect(() => {
    cancelledRef.current = false
    connect()
    return () => {
      // Order matters: latch cancelled BEFORE tearing down, so an in-flight
      // connect() that resumes after this cleanup bails out instead of
      // resurrecting a peer connection on a window the user has closed.
      cancelledRef.current = true
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = null
      pcRef.current?.close()
      wsRef.current?.close()
      if (gpLoopRef.current) cancelAnimationFrame(gpLoopRef.current)
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-retry on error. Deliberately does NOT run once the viewer has given
  // up: re-entering connect() clears `error`, so an unconditional interval
  // would alternate the verdict with a spinner forever.
  useEffect(() => {
    if (!error || gaveUp) return
    const id = setInterval(() => connect(), 2000)
    return () => clearInterval(id)
  }, [error, gaveUp, connect])

  const retryNow = useCallback(() => {
    startAttemptsRef.current = 0
    setGaveUp(false)
    setStatus('starting')
    connect()
  }, [connect])

  // Gamepad polling loop — reads full state at rAF rate and sends via dedicated channel.
  // Only polls when a gamepad is actually connected; stops cleanly on unmount or disconnect.
  useEffect(() => {
    if (status !== 'connected') return

    let prevButtons: boolean[] = []
    let prevAxes: number[] = []

    const pollGamepad = () => {
      const gpDc = gpDcRef.current
      if (!gpDc || gpDc.readyState !== 'open') {
        gpLoopRef.current = requestAnimationFrame(pollGamepad)
        return
      }

      const gamepads = navigator.getGamepads?.() || []
      const gp = gamepads[0] // primary gamepad only
      if (!gp) {
        // No gamepad connected — keep loop alive so we detect a future connection
        gpLoopRef.current = requestAnimationFrame(pollGamepad)
        return
      }

      // Build state snapshot matching backend handleGamepad struct:
      // buttons []bool, axes []float64, triggers []float64
      const buttons = gp.buttons.map(b => b.pressed)
      const axes = gp.axes.map(a => (Math.abs(a) < 0.05 ? 0 : Math.round(a * 1000) / 1000))
      // Triggers are buttons 6 (LT) and 7 (RT) in the standard gamepad mapping
      const triggers = [gp.buttons[6]?.value ?? 0, gp.buttons[7]?.value ?? 0]

      const buttonsChanged = buttons.some((b, i) => b !== prevButtons[i])
      const axesChanged = axes.some((a, i) => Math.abs(a - (prevAxes[i] ?? 0)) > 0.01)

      if (buttonsChanged || axesChanged) {
        gpDc.send(JSON.stringify({ buttons, axes, triggers }))
        prevButtons = buttons
        prevAxes = axes
      }

      gpLoopRef.current = requestAnimationFrame(pollGamepad)
    }

    gpLoopRef.current = requestAnimationFrame(pollGamepad)
    return () => { if (gpLoopRef.current) cancelAnimationFrame(gpLoopRef.current) }
  }, [status])

  // Dynamically resize Xvfb to match container size
  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    let resizeTimer: ReturnType<typeof setTimeout> | null = null
    const observer = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect
      const w = Math.round(width)
      const h = Math.round(height)
      if (w < 320 || h < 200) return
      if (resizeTimer) clearTimeout(resizeTimer)
      resizeTimer = setTimeout(() => {
        streamSize.current = { w, h }
        fetch('/api/stream/resize', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: sessionId, width: w, height: h }),
        }).catch(() => {})
      }, 300)
    })
    observer.observe(el)
    return () => { observer.disconnect(); if (resizeTimer) clearTimeout(resizeTimer) }
  }, [sessionId])

  // Poll adaptive quality tier from session metadata — GAME-08 toolbar.
  // The backend exposes `quality` in the session JSON (set by bitrateController).
  useEffect(() => {
    const id = setInterval(async () => {
      try {
        const res = await fetch('/api/stream/sessions')
        const sessions = toStreamSessions(await res.json())
        const sess = sessions.find(s => s.id === sessionId)
        if (sess?.quality) setStreamToolbarQuality(sess.quality)
      } catch {
        // non-fatal
      }
    }, 4000)
    return () => clearInterval(id)
  }, [sessionId])

  const focusContainer = useCallback(() => containerRef.current?.focus(), [])

  const getPos = useCallback((e: ReactMouseEvent<HTMLDivElement>) => {
    const video = videoRef.current
    if (!video) return { x: 0, y: 0 }
    const rect = video.getBoundingClientRect()
    const vidW = video.videoWidth || streamSize.current.w
    const vidH = video.videoHeight || streamSize.current.h
    const videoAspect = vidW / vidH
    const elemAspect = rect.width / rect.height
    let renderW, renderH, offsetX, offsetY
    if (elemAspect > videoAspect) {
      renderH = rect.height
      renderW = rect.height * videoAspect
      offsetX = (rect.width - renderW) / 2
      offsetY = 0
    } else {
      renderW = rect.width
      renderH = rect.width / videoAspect
      offsetX = 0
      offsetY = (rect.height - renderH) / 2
    }
    return {
      x: Math.round(((e.clientX - rect.left - offsetX) / renderW) * vidW),
      y: Math.round(((e.clientY - rect.top - offsetY) / renderH) * vidH)
    }
  }, [])

  // Pointer lock lifecycle — track lock state via document events.
  // pointerLockedRef is used in hot-path handlers; pointerLocked state drives UI.
  useEffect(() => {
    const onLockChange = () => {
      const locked = document.pointerLockElement === containerRef.current ||
                     document.pointerLockElement === videoRef.current
      pointerLockedRef.current = locked
      setPointerLocked(locked)
    }
    const onLockError = () => {
      pointerLockedRef.current = false
      setPointerLocked(false)
    }
    document.addEventListener('pointerlockchange', onLockChange)
    document.addEventListener('pointerlockerror', onLockError)
    return () => {
      document.removeEventListener('pointerlockchange', onLockChange)
      document.removeEventListener('pointerlockerror', onLockError)
      // Release lock if component unmounts while locked
      if (pointerLockedRef.current) document.exitPointerLock?.()
    }
  }, [])

  // --- Mouse events (unreliable channel) ---
  const onMouseMove = useCallback((e: ReactMouseEvent<HTMLDivElement>) => {
    if (pointerLockedRef.current) {
      // Pointer-lock mode: bypass coalesce throttle, send raw deltas immediately
      const dx = e.movementX
      const dy = e.movementY
      if (dx !== 0 || dy !== 0) {
        sendMouse({ t: 'mr', dx, dy })
      }
      return
    }
    const now = performance.now()
    if (now - lastMouseRef.current < 8) return // ~120hz cap
    lastMouseRef.current = now
    sendMouse({ t: 'mm', ...getPos(e) })
  }, [getPos, sendMouse])

  const onMouseDown = useCallback((e: ReactMouseEvent<HTMLDivElement>) => {
    e.preventDefault()
    focusContainer()
    // In gaming mode, first click acquires pointer lock instead of sending a button event
    if (gaming && !pointerLockedRef.current && status === 'connected') {
      const el = containerRef.current
      if (el) el.requestPointerLock()
      return
    }
    sendMouse({ t: 'md', ...getPos(e), b: e.button })
  }, [gaming, status, getPos, sendMouse, focusContainer])

  const onMouseUp = useCallback((e: ReactMouseEvent<HTMLDivElement>) => sendMouse({ t: 'mu', b: e.button }), [sendMouse])

  const onWheel = useCallback((e: ReactWheelEvent<HTMLDivElement>) => {
    e.preventDefault()
    scrollAccRef.current += e.deltaY * scrollSensitivity
    if (scrollRafRef.current) return
    scrollRafRef.current = requestAnimationFrame(() => {
      const acc = scrollAccRef.current
      scrollAccRef.current = 0
      scrollRafRef.current = null
      if (Math.abs(acc) < 1) return
      const clicks = Math.min(10, Math.max(1, Math.round(Math.abs(acc) / 50)))
      sendMouse({ t: 'sc', y: acc > 0 ? clicks : -clicks })
    })
  }, [sendMouse, scrollSensitivity])

  // --- Keyboard events (reliable channel) ---
  const onKeyDown = useCallback((e: ReactKeyboardEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.stopPropagation()
    // Escape releases pointer lock in gaming mode; the keydown is NOT forwarded
    // so the remote app does not receive an unintended Escape while unlocking.
    if (gaming && e.key === 'Escape' && pointerLockedRef.current) {
      document.exitPointerLock?.()
      return
    }
    sendKbd({ t: 'kd', key: e.key, code: e.code, mod: getModifiers(e) })
  }, [gaming, sendKbd])

  const onKeyUp = useCallback((e: ReactKeyboardEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.stopPropagation()
    sendKbd({ t: 'ku', key: e.key, code: e.code, mod: getModifiers(e) })
  }, [sendKbd])

  useEffect(() => {
    if (status === 'connected') focusContainer()
  }, [status, focusContainer])

  // STREAM-01: this branch used to render a spinner over the words "Starting
  // app...", with the actual reason demoted to 12px at 40% opacity underneath.
  // Every failure the viewer can reach — a refused signalling socket, a dead
  // ICE path, a 500 from the box, a session that never started — was therefore
  // presented to the user as ordinary progress, which is why a permanently
  // broken stream "just says connecting". An error is not a loading state: no
  // spinner, the reason is the headline, and there is a way out.
  if (error) {
    return (
      <div
        role="alert"
        className="flex items-center justify-center h-full w-full min-w-0 bg-black text-white/50 text-sm p-6"
      >
        <div className="stream-fade-in text-center space-y-3 max-w-sm min-w-0">
          <span aria-hidden="true" className="block text-2xl leading-none text-white/30">⚠</span>
          <p className="text-white/80 font-medium">Stream unavailable</p>
          <p className="text-white/50 text-xs break-words">{error}</p>
          <button
            onClick={retryNow}
            className="mt-1 px-3 py-1.5 rounded-lg bg-white/10 text-white/80 text-xs font-medium hover:bg-white/20 transition-[background-color] duration-[var(--motion-fast)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--accent)]"
          >
            {gaveUp ? 'Try again' : 'Retry now'}
          </button>
          {!gaveUp && (
            <p className="text-white/30 text-[11px]">Retrying automatically…</p>
          )}
        </div>
      </div>
    )
  }

  return (
    <div
      ref={containerRef}
      className="w-full h-full bg-black outline-none overflow-hidden relative"
      tabIndex={0}
      onMouseMove={onMouseMove}
      onMouseDown={onMouseDown}
      onMouseUp={onMouseUp}
      onWheel={onWheel}
      onKeyDown={onKeyDown}
      onKeyUp={onKeyUp}
      onContextMenu={e => e.preventDefault()}
      onClick={focusContainer}
      style={{ cursor: pointerLocked ? 'none' : undefined }}
    >
      <style>{`
        @keyframes streamFadeIn {
          from { opacity: 0; transform: translateY(-4px); }
          to   { opacity: 1; transform: none; }
        }
        @keyframes streamPulse {
          0%, 100% { opacity: 1; }
          50%      { opacity: 0.35; }
        }
        .stream-fade-in { animation: streamFadeIn var(--motion-base) var(--ease-out); }
        .stream-pulse   { animation: streamPulse 2s var(--ease-standard) infinite; }
        @media (prefers-reduced-motion: reduce) {
          .stream-fade-in, .stream-pulse { animation: none; }
        }
      `}</style>

      {/* Stream toolbar overlay — GAME-08 (additive, does not touch existing layout) */}
      {status === 'connected' && (
        <StreamToolbar
          sessionId={sessionId}
          pcRef={pcRef}
          quality={streamToolbarQuality}
          streamToolbarCollapsed={streamToolbarCollapsed}
          onStreamToolbarToggle={() => setStreamToolbarCollapsed(c => !c)}
        />
      )}

      {status !== 'connected' && (
        <div className="stream-fade-in absolute inset-0 flex items-center justify-center z-10 bg-black">
          <span className="text-white/55 text-sm flex items-center gap-2.5">
            <span className="w-4 h-4 spinner" />
            {status === 'starting' ? 'Starting app…' : 'Connecting…'}
          </span>
        </div>
      )}
      {gaming && !pointerLocked && status === 'connected' && (
        <div className="absolute inset-x-0 bottom-6 flex items-center justify-center z-10 px-4 pointer-events-none">
          <div className="stream-fade-in max-w-full bg-black/55 text-white/85 text-xs px-3.5 py-2 rounded-full backdrop-blur-md ring-1 ring-white/10 shadow-lg shadow-black/30 select-none">
            Click to capture mouse &mdash; Esc to release
          </div>
        </div>
      )}
      <video
        ref={videoRef}
        autoPlay playsInline
        disablePictureInPicture
        controlsList="noplaybackrate nodownload"
        className="absolute inset-0 w-full h-full"
        style={{ cursor: pointerLocked ? 'none' : 'default', objectFit: 'contain' }}
      />
    </div>
  )
}
