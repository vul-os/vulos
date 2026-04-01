import { useState, useEffect, useRef, useCallback } from 'react'

export default function RemoteBrowser() {
  const videoRef = useRef(null)
  const wsRef = useRef(null)
  const pcRef = useRef(null)
  const dcRef = useRef(null)
  const gpDcRef = useRef(null) // gamepad data channel
  const gpLoopRef = useRef(null)
  const containerRef = useRef(null)
  const connectedRef = useRef(false)
  const lastMouseRef = useRef(0)
  const lastScrollRef = useRef(0)
  const [status, setStatus] = useState('connecting')
  const [error, setError] = useState(null)
  const isEmbedded = navigator.userAgent.includes('WPE') || navigator.userAgent.includes('Cog')


  const mouseDcRef = useRef(null)
  const kbdDcRef = useRef(null)

  const sendMouse = useCallback((evt) => {
    const dc = mouseDcRef.current
    if (!dc || dc.readyState !== 'open') {
      // Fallback to legacy channel
      const ldc = dcRef.current
      if (ldc && ldc.readyState === 'open') ldc.send(JSON.stringify(evt))
      return
    }
    dc.send(JSON.stringify(evt))
  }, [])

  const sendKbd = useCallback((evt) => {
    const dc = kbdDcRef.current
    if (!dc || dc.readyState !== 'open') {
      const ldc = dcRef.current
      if (ldc && ldc.readyState === 'open') ldc.send(JSON.stringify(evt))
      return
    }
    dc.send(JSON.stringify(evt))
  }, [])

  // Keep legacy sendInput for touch gestures that mix mouse events
  const sendInput = useCallback((evt) => {
    // Route to appropriate channel
    if (evt.type === 'keydown' || evt.type === 'keyup') {
      sendKbd({ t: evt.type === 'keydown' ? 'kd' : 'ku', key: evt.key, code: evt.code, mod: 0 })
    } else {
      const t = { mousemove: 'mm', mousedown: 'md', mouseup: 'mu', click: 'md', scroll: 'sc' }[evt.type] || evt.type
      sendMouse({ t, x: evt.x, y: evt.y, b: evt.button || 0 })
      if (evt.type === 'click') sendMouse({ t: 'mu', b: evt.button || 0 })
    }
  }, [sendMouse, sendKbd])

  const connect = useCallback(async () => {
    pcRef.current?.close()
    wsRef.current?.close()
    connectedRef.current = false
    setStatus('connecting')
    setError(null)

    try {
      const res = await fetch('/api/browser/status')
      const data = await res.json()
      if (!data.running) {
        setError('Browser service not running')
        return
      }

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

      // Mouse channel — unreliable/unordered (latest-wins)
      const mouseDc = pc.createDataChannel('mouse', { ordered: false, maxRetransmits: 0 })
      mouseDcRef.current = mouseDc

      // Keyboard channel — reliable/ordered (every event must arrive)
      const kbdDc = pc.createDataChannel('keyboard', { ordered: true })
      kbdDcRef.current = kbdDc

      // Legacy input channel (fallback)
      const dc = pc.createDataChannel('input', { ordered: false, maxRetransmits: 0 })
      dcRef.current = dc

      // Gamepad channel — separate unreliable channel
      const gpDc = pc.createDataChannel('gamepad', { ordered: false, maxRetransmits: 0 })
      gpDcRef.current = gpDc

      pc.addTransceiver('video', { direction: 'recvonly' })

      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/api/browser/ws`)
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
  }, [])

  useEffect(() => {
    connect()
    return () => { pcRef.current?.close(); wsRef.current?.close(); if (gpLoopRef.current) cancelAnimationFrame(gpLoopRef.current) }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const focusContainer = useCallback(() => containerRef.current?.focus(), [])

  const getPos = useCallback((e) => {
    const video = videoRef.current
    if (!video) return { x: 0, y: 0 }
    const rect = video.getBoundingClientRect()
    // Account for letterboxing from object-fit: contain
    const videoAspect = 1280 / 720
    const elemAspect = rect.width / rect.height
    let renderW, renderH, offsetX, offsetY
    if (elemAspect > videoAspect) {
      // Letterboxed on sides
      renderH = rect.height
      renderW = rect.height * videoAspect
      offsetX = (rect.width - renderW) / 2
      offsetY = 0
    } else {
      // Letterboxed top/bottom
      renderW = rect.width
      renderH = rect.width / videoAspect
      offsetX = 0
      offsetY = (rect.height - renderH) / 2
    }
    return {
      x: Math.round(((e.clientX - rect.left - offsetX) / renderW) * 1280),
      y: Math.round(((e.clientY - rect.top - offsetY) / renderH) * 720)
    }
  }, [])

  const onMouseMove = useCallback((e) => {
    const now = performance.now()
    if (now - lastMouseRef.current < 16) return
    lastMouseRef.current = now
    sendInput({ type: 'mousemove', ...getPos(e) })
  }, [getPos, sendInput])

  const onMouseDown = useCallback((e) => {
    e.preventDefault()
    focusContainer()
    sendInput({ type: 'mousedown', ...getPos(e), button: e.button })
  }, [getPos, sendInput, focusContainer])

  const onMouseUp = useCallback((e) => sendInput({ type: 'mouseup', button: e.button }), [sendInput])

  // Scroll coalescing — accumulate delta over 16ms frames then send one event
  const scrollAccRef = useRef(0)
  const scrollRafRef = useRef(null)

  const onWheel = useCallback((e) => {
    e.preventDefault()
    // Normalize deltaY across browsers/trackpads
    let dy = e.deltaY
    if (e.deltaMode === 1) dy *= 40 // line mode
    else if (e.deltaMode === 2) dy *= 800 // page mode
    scrollAccRef.current += dy

    if (scrollRafRef.current) return // Already have a frame queued
    scrollRafRef.current = requestAnimationFrame(() => {
      const acc = scrollAccRef.current
      scrollAccRef.current = 0
      scrollRafRef.current = null
      if (Math.abs(acc) < 1) return
      // Convert accumulated pixel delta to scroll clicks (clamped 1-10)
      const clicks = Math.min(10, Math.max(1, Math.round(Math.abs(acc) / 30)))
      sendInput({ type: 'scroll', x: 0, y: acc > 0 ? clicks : -clicks })
    })
  }, [sendInput])

  // Touch gesture handling — translate touch events to mouse/scroll
  const touchRef = useRef({ startX: 0, startY: 0, lastX: 0, lastY: 0, startTime: 0, fingers: 0 })
  const momentumRef = useRef(null)

  const onTouchStart = useCallback((e) => {
    e.preventDefault()
    focusContainer()
    if (momentumRef.current) { cancelAnimationFrame(momentumRef.current); momentumRef.current = null }
    const t = e.touches[0]
    touchRef.current = { startX: t.clientX, startY: t.clientY, lastX: t.clientX, lastY: t.clientY, startTime: performance.now(), fingers: e.touches.length, velocityY: 0 }
    if (e.touches.length === 1) {
      sendInput({ type: 'mousemove', ...getPos(t) })
    }
  }, [getPos, sendInput, focusContainer])

  const onTouchMove = useCallback((e) => {
    e.preventDefault()
    const t = e.touches[0]
    const touch = touchRef.current

    if (e.touches.length >= 2) {
      // Two-finger scroll
      const dy = touch.lastY - t.clientY
      touch.velocityY = dy
      touch.lastY = t.clientY
      touch.lastX = t.clientX
      if (Math.abs(dy) > 1) {
        const clicks = Math.min(5, Math.max(1, Math.round(Math.abs(dy) / 15)))
        sendInput({ type: 'scroll', x: 0, y: dy > 0 ? clicks : -clicks })
      }
    } else {
      // Single finger — move cursor
      const now = performance.now()
      if (now - lastMouseRef.current < 16) return
      lastMouseRef.current = now
      touch.lastX = t.clientX
      touch.lastY = t.clientY
      sendInput({ type: 'mousemove', ...getPos(t) })
    }
  }, [getPos, sendInput])

  const onTouchEnd = useCallback((e) => {
    e.preventDefault()
    const touch = touchRef.current
    const elapsed = performance.now() - touch.startTime
    const dx = touch.lastX - touch.startX
    const dy = touch.lastY - touch.startY
    const dist = Math.sqrt(dx * dx + dy * dy)

    // Tap detection (short press, minimal movement)
    if (touch.fingers === 1 && elapsed < 300 && dist < 15) {
      const pos = getPos({ clientX: touch.startX, clientY: touch.startY })
      sendInput({ type: 'click', ...pos, button: 0 })
      return
    }

    // Long press = right click
    if (touch.fingers === 1 && elapsed > 500 && dist < 15) {
      const pos = getPos({ clientX: touch.startX, clientY: touch.startY })
      sendInput({ type: 'click', ...pos, button: 2 })
      return
    }

    // Momentum scrolling for two-finger gesture
    if (touch.fingers >= 2 && Math.abs(touch.velocityY) > 2) {
      let vel = touch.velocityY
      const decay = () => {
        vel *= 0.92
        if (Math.abs(vel) < 0.5) { momentumRef.current = null; return }
        const clicks = Math.min(3, Math.max(1, Math.round(Math.abs(vel) / 15)))
        sendInput({ type: 'scroll', x: 0, y: vel > 0 ? clicks : -clicks })
        momentumRef.current = requestAnimationFrame(decay)
      }
      momentumRef.current = requestAnimationFrame(decay)
    }
  }, [getPos, sendInput])

  const onKeyDown = useCallback((e) => {
    // Let Escape bubble to shell for window management
    if (e.key === 'Escape') return
    e.preventDefault()
    e.stopPropagation()
    let m = 0
    if (e.shiftKey) m |= 1
    if (e.ctrlKey)  m |= 2
    if (e.altKey)   m |= 4
    if (e.metaKey)  m |= 8
    if (e.getModifierState?.('CapsLock')) m |= 16
    sendKbd({ t: 'kd', key: e.key, code: e.code, mod: m })
  }, [sendKbd])

  const onKeyUp = useCallback((e) => {
    if (e.key === 'Escape') return
    e.preventDefault()
    e.stopPropagation()
    let m = 0
    if (e.shiftKey) m |= 1
    if (e.ctrlKey)  m |= 2
    if (e.altKey)   m |= 4
    if (e.metaKey)  m |= 8
    if (e.getModifierState?.('CapsLock')) m |= 16
    sendKbd({ t: 'ku', key: e.key, code: e.code, mod: m })
  }, [sendKbd])

  useEffect(() => {
    if (status === 'connected') focusContainer()
  }, [status, focusContainer])

  // Gamepad polling loop — reads state at 60fps and sends via dedicated channel
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
      const gp = gamepads[0] // primary gamepad
      if (!gp) {
        gpLoopRef.current = requestAnimationFrame(pollGamepad)
        return
      }

      // Only send if state changed (avoid flooding)
      const buttons = gp.buttons.map(b => b.pressed)
      const axes = gp.axes.map(a => Math.abs(a) < 0.05 ? 0 : Math.round(a * 1000) / 1000)
      // Triggers are buttons 6 and 7 in standard mapping, but have analog values
      const triggers = [gp.buttons[6]?.value || 0, gp.buttons[7]?.value || 0]

      const buttonsChanged = buttons.some((b, i) => b !== prevButtons[i])
      const axesChanged = axes.some((a, i) => Math.abs(a - (prevAxes[i] || 0)) > 0.01)

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

  // Auto-retry when browser service isn't running (e.g. after redeploy)
  useEffect(() => {
    if (!error) return
    const id = setInterval(() => connect(), 5000)
    return () => clearInterval(id)
  }, [error, connect])

  // Embedded WebKit (bare metal) — use system browser directly
  if (isEmbedded) return <LocalBrowser />

  if (error) {
    return (
      <div className="flex items-center justify-center h-full bg-neutral-950 text-neutral-500 text-sm">
        <div className="text-center space-y-3">
          <span className="w-6 h-6 border-2 border-neutral-700 border-t-blue-500 rounded-full animate-spin inline-block" />
          <p className="text-neutral-400">Starting browser...</p>
          <p className="text-neutral-600 text-xs">{error}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="w-full h-full bg-neutral-950 flex flex-col overflow-hidden">
      <div
        ref={containerRef}
        className="flex-1 relative outline-none overflow-hidden"
        tabIndex={0}
        onMouseMove={onMouseMove}
        onMouseDown={onMouseDown}
        onMouseUp={onMouseUp}
        onWheel={onWheel}
        onTouchStart={onTouchStart}
        onTouchMove={onTouchMove}
      onTouchEnd={onTouchEnd}
      onKeyDown={onKeyDown}
      onKeyUp={onKeyUp}
      onContextMenu={e => e.preventDefault()}
      onClick={focusContainer}
    >
      {status === 'connecting' && (
        <div className="absolute inset-0 flex items-center justify-center z-10">
          <span className="text-neutral-600 text-sm flex items-center gap-2">
            <span className="w-4 h-4 border-2 border-neutral-600 border-t-blue-500 rounded-full animate-spin" />
            Connecting...
          </span>
        </div>
      )}
      <video
        ref={videoRef}
        autoPlay playsInline
        disablePictureInPicture
        controlsList="noplaybackrate nodownload"
        className="w-full h-full"
        style={{ cursor: 'none', objectFit: 'contain', background: '#000' }}
      />
      </div>
    </div>
  )
}

function LocalBrowser() {
  const [url, setUrl] = useState('https://google.com')
  return (
    <div className="flex flex-col h-full bg-neutral-950">
      <div className="flex items-center gap-2 px-3 py-2 bg-neutral-900 border-b border-neutral-800/50">
        <input
          value={url} onChange={e => setUrl(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') window.open(url, '_blank') }}
          placeholder="Enter URL..." autoFocus
          className="flex-1 bg-neutral-800/60 border border-neutral-700/50 rounded-lg px-3 py-2 text-sm text-white outline-none"
        />
        <button onClick={() => window.open(url, '_blank')} className="btn">Open</button>
      </div>
      <div className="flex-1 flex items-center justify-center text-neutral-600 text-sm">
        URLs open in the system browser (WebKit)
      </div>
    </div>
  )
}
