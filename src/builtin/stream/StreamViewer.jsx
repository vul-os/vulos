import { useState, useEffect, useRef, useCallback } from 'react'

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

      pc.addTransceiver('video', { direction: 'recvonly' })

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
  }, [sessionId])

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
      <div className="flex items-center justify-center h-full bg-neutral-950 text-neutral-500 text-sm">
        <div className="text-center space-y-3">
          <span className="w-6 h-6 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin inline-block" />
          <p className="text-neutral-400">Starting app...</p>
          <p className="text-neutral-600 text-xs">{error}</p>
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
      {status === 'connecting' && (
        <div className="absolute inset-0 flex items-center justify-center z-10 bg-neutral-950">
          <span className="text-neutral-600 text-sm flex items-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-500 rounded-full animate-spin" />
            Connecting...
          </span>
        </div>
      )}
      {gaming && !pointerLocked && status === 'connected' && (
        <div className="absolute inset-0 flex items-center justify-center z-10 pointer-events-none">
          <div className="bg-black/60 text-neutral-300 text-xs px-3 py-1.5 rounded-full backdrop-blur-sm select-none">
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
