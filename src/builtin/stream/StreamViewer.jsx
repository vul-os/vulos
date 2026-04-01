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

export default function StreamViewer({ sessionId, scrollSensitivity = 1.0 }) {
  const videoRef = useRef(null)
  const wsRef = useRef(null)
  const pcRef = useRef(null)
  const mouseDcRef = useRef(null)
  const kbdDcRef = useRef(null)
  const containerRef = useRef(null)
  const connectedRef = useRef(false)
  const lastMouseRef = useRef(0)
  const scrollAccRef = useRef(0)
  const scrollRafRef = useRef(null)
  const [status, setStatus] = useState('connecting')
  const [error, setError] = useState(null)
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
    return () => { pcRef.current?.close(); wsRef.current?.close() }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-retry on error
  useEffect(() => {
    if (!error) return
    const id = setInterval(() => connect(), 2000)
    return () => clearInterval(id)
  }, [error, connect])

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

  // --- Mouse events (unreliable channel) ---
  const onMouseMove = useCallback((e) => {
    const now = performance.now()
    if (now - lastMouseRef.current < 8) return // ~120hz cap
    lastMouseRef.current = now
    sendMouse({ t: 'mm', ...getPos(e) })
  }, [getPos, sendMouse])

  const onMouseDown = useCallback((e) => {
    e.preventDefault()
    focusContainer()
    sendMouse({ t: 'md', ...getPos(e), b: e.button })
  }, [getPos, sendMouse, focusContainer])

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
    sendKbd({ t: 'kd', key: e.key, code: e.code, mod: getModifiers(e) })
  }, [sendKbd])

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
    >
      {status === 'connecting' && (
        <div className="absolute inset-0 flex items-center justify-center z-10 bg-neutral-950">
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
        className="absolute inset-0 w-full h-full"
        style={{ cursor: 'default', objectFit: 'contain' }}
      />
    </div>
  )
}
