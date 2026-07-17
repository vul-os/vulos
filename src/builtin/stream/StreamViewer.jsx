import { useState, useEffect, useRef, useCallback } from 'react'
import { applyLowLatencyHints } from './lowLatency'

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

// ---------------------------------------------------------------------------
// StreamToolbar — gamer overlay: FPS selector, live RTT/quality, fullscreen,
// MangoHud toggle.  All identifiers are gameToolbar-/streamToolbar- prefixed
// to avoid collisions with existing component state.
// ---------------------------------------------------------------------------

const STREAM_TOOLBAR_FPS_OPTIONS = [30, 60, 90, 120]

function StreamToolbar({
  sessionId,
  pcRef,
  quality,
  streamToolbarCollapsed,
  onStreamToolbarToggle,
}) {
  const [streamToolbarFps, setStreamToolbarFps] = useState(30)
  const [streamToolbarRtt, setStreamToolbarRtt] = useState(null)
  const [streamToolbarMangoHud, setStreamToolbarMangoHud] = useState(false)
  const [streamToolbarFullscreen, setStreamToolbarFullscreen] = useState(false)
  const streamToolbarRttRef = useRef(null)

  // Poll ICE candidate-pair RTT from WebRTC stats every 2 s
  useEffect(() => {
    streamToolbarRttRef.current = setInterval(async () => {
      const pc = pcRef.current
      if (!pc) return
      try {
        const stats = await pc.getStats()
        stats.forEach((report) => {
          if (
            report.type === 'candidate-pair' &&
            report.state === 'succeeded' &&
            report.nominated &&
            report.currentRoundTripTime != null
          ) {
            setStreamToolbarRtt(Math.round(report.currentRoundTripTime * 1000))
          }
        })
      } catch {
        // getStats can throw if PC is closing — ignore
      }
    }, 2000)
    return () => clearInterval(streamToolbarRttRef.current)
  }, [pcRef])

  // Track fullscreen state from browser events
  useEffect(() => {
    const onFsChange = () => setStreamToolbarFullscreen(!!document.fullscreenElement)
    document.addEventListener('fullscreenchange', onFsChange)
    return () => document.removeEventListener('fullscreenchange', onFsChange)
  }, [])

  const streamToolbarSetFps = useCallback(async (fps) => {
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
    streamToolbarRtt < 50   ? 'text-[var(--status-success)]' :
    streamToolbarRtt < 120  ? 'text-[var(--status-warning)]' :
                               'text-[var(--status-danger)]'

  const streamToolbarQualityColor =
    quality === 'max'    ? 'text-[var(--status-success)]' :
    quality === 'high'   ? 'text-[var(--status-success)]' :
    quality === 'medium' ? 'text-[var(--status-warning)]' :
    quality === 'low'    ? 'text-[var(--status-danger)]' :
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

function getModifiers(e) {
  let m = 0
  if (e.shiftKey) m |= MOD_SHIFT
  if (e.ctrlKey)  m |= MOD_CTRL
  if (e.altKey)   m |= MOD_ALT
  if (e.metaKey)  m |= MOD_META
  if (e.getModifierState?.('CapsLock')) m |= MOD_CAPSLOCK
  return m
}

export default function StreamViewer({ sessionId, scrollSensitivity = 1.0, gaming = false }) {
  const videoRef = useRef(null)
  const wsRef = useRef(null)
  const pcRef = useRef(null)
  const mouseDcRef = useRef(null)
  const kbdDcRef = useRef(null)
  const gpDcRef = useRef(null)
  const gpLoopRef = useRef(null)
  const containerRef = useRef(null)
  const connectedRef = useRef(false)
  const lastMouseRef = useRef(0)
  const scrollAccRef = useRef(0)
  const scrollRafRef = useRef(null)
  const pointerLockedRef = useRef(false)
  const [status, setStatus] = useState('connecting')
  const [error, setError] = useState(null)
  const [pointerLocked, setPointerLocked] = useState(false)
  const streamSize = useRef({ w: 1280, h: 720 })

  // Stream toolbar state — GAME-08
  const [streamToolbarQuality, setStreamToolbarQuality] = useState(null)
  const [streamToolbarCollapsed, setStreamToolbarCollapsed] = useState(false)

  const sendMouse = useCallback((evt) => {
    const dc = mouseDcRef.current
    if (!dc || dc.readyState !== 'open') return
    dc.send(JSON.stringify(evt))
  }, [])

  const sendKbd = useCallback((evt) => {
    const dc = kbdDcRef.current
    if (!dc || dc.readyState !== 'open') return
    dc.send(JSON.stringify(evt))
  }, [])

  const connect = useCallback(async () => {
    pcRef.current?.close()
    wsRef.current?.close()
    connectedRef.current = false
    setStatus('connecting')
    setError(null)

    try {
      const res = await fetch('/api/stream/sessions')
      const sessions = await res.json()
      const session = sessions?.find(s => s.id === sessionId)
      if (!session || !session.running) {
        // eslint-disable-next-line react-hooks/immutability
        setTimeout(() => connect(), 1000)
        return
      }
      streamSize.current = { w: session.width || 1280, h: session.height || 720 }

      const pc = new RTCPeerConnection({
        iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
      })
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
        let msg
        try { msg = JSON.parse(e.data) } catch { return }
        if (msg.type === 'answer') pc.setRemoteDescription(new RTCSessionDescription({ type: 'answer', sdp: msg.sdp }))
        else if (msg.type === 'candidate' && msg.candidate) pc.addIceCandidate(new RTCIceCandidate(msg.candidate))
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
      setError(err.message)
    }
  }, [sessionId, gaming])

  useEffect(() => {
    connect()
    return () => {
      pcRef.current?.close()
      wsRef.current?.close()
      if (gpLoopRef.current) cancelAnimationFrame(gpLoopRef.current)
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-retry on error
  useEffect(() => {
    if (!error) return
    const id = setInterval(() => connect(), 2000)
    return () => clearInterval(id)
  }, [error, connect])

  // Gamepad polling loop — reads full state at rAF rate and sends via dedicated channel.
  // Only polls when a gamepad is actually connected; stops cleanly on unmount or disconnect.
  useEffect(() => {
    if (status !== 'connected') return

    let prevButtons = []
    let prevAxes = []

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
    let resizeTimer = null
    const observer = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect
      const w = Math.round(width)
      const h = Math.round(height)
      if (w < 320 || h < 200) return
      clearTimeout(resizeTimer)
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
    return () => { observer.disconnect(); clearTimeout(resizeTimer) }
  }, [sessionId])

  // Poll adaptive quality tier from session metadata — GAME-08 toolbar.
  // The backend exposes `quality` in the session JSON (set by bitrateController).
  useEffect(() => {
    const id = setInterval(async () => {
      try {
        const res = await fetch('/api/stream/sessions')
        const sessions = await res.json()
        const sess = sessions?.find(s => s.id === sessionId)
        if (sess?.quality) setStreamToolbarQuality(sess.quality)
      } catch {
        // non-fatal
      }
    }, 4000)
    return () => clearInterval(id)
  }, [sessionId])

  const focusContainer = useCallback(() => containerRef.current?.focus(), [])

  const getPos = useCallback((e) => {
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
  const onMouseMove = useCallback((e) => {
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

  const onMouseDown = useCallback((e) => {
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

  const onMouseUp = useCallback((e) => sendMouse({ t: 'mu', b: e.button }), [sendMouse])

  const onWheel = useCallback((e) => {
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
  const onKeyDown = useCallback((e) => {
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

  const onKeyUp = useCallback((e) => {
    e.preventDefault()
    e.stopPropagation()
    sendKbd({ t: 'ku', key: e.key, code: e.code, mod: getModifiers(e) })
  }, [sendKbd])

  useEffect(() => {
    if (status === 'connected') focusContainer()
  }, [status, focusContainer])

  if (error) {
    return (
      <div className="flex items-center justify-center h-full w-full min-w-0 bg-black text-white/50 text-sm p-6">
        <div className="stream-fade-in text-center space-y-3 max-w-sm min-w-0">
          <span className="w-6 h-6 spinner inline-block" />
          <p className="text-white/70">Starting app...</p>
          <p className="text-white/40 text-xs break-words">{error}</p>
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

      {status === 'connecting' && (
        <div className="stream-fade-in absolute inset-0 flex items-center justify-center z-10 bg-black">
          <span className="text-white/55 text-sm flex items-center gap-2.5">
            <span className="w-4 h-4 spinner" />
            Connecting...
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
