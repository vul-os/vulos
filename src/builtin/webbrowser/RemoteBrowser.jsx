import { useState, useEffect, useRef, useCallback } from 'react'

export default function RemoteBrowser() {
  const videoRef = useRef(null)
  const wsRef = useRef(null)
  const pcRef = useRef(null)
  const dcRef = useRef(null)
  const containerRef = useRef(null)
  const connectedRef = useRef(false)
  const lastMouseRef = useRef(0)
  const lastScrollRef = useRef(0)
  const [status, setStatus] = useState('connecting')
  const [error, setError] = useState(null)

  if (navigator.userAgent.includes('WPE') || navigator.userAgent.includes('Cog')) {
    return <LocalBrowser />
  }

  const sendInput = useCallback((evt) => {
    const dc = dcRef.current
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

      const dc = pc.createDataChannel('input', { ordered: false, maxRetransmits: 0 })
      dcRef.current = dc
      pc.addTransceiver('video', { direction: 'recvonly' })

      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
      const ws = new WebSocket(`${proto}//${location.host}/api/browser/ws`)
      wsRef.current = ws

      ws.onmessage = (e) => {
        const msg = JSON.parse(e.data)
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
    return () => { pcRef.current?.close(); wsRef.current?.close() }
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

  const onWheel = useCallback((e) => {
    e.preventDefault()
    const now = performance.now()
    if (now - lastScrollRef.current < 50) return
    lastScrollRef.current = now
    // Normalize deltaY across browsers/trackpads — clamp to reasonable range
    let dy = e.deltaY
    if (e.deltaMode === 1) dy *= 40 // line mode
    else if (e.deltaMode === 2) dy *= 800 // page mode
    // Convert pixel delta to scroll clicks (3 pixels per click, clamped 1-5)
    const clicks = Math.min(5, Math.max(1, Math.round(Math.abs(dy) / 40)))
    sendInput({ type: 'scroll', x: 0, y: dy > 0 ? clicks : -clicks })
  }, [sendInput])

  const onKeyDown = useCallback((e) => {
    // Let Escape bubble to shell for window management
    if (e.key === 'Escape') return
    e.preventDefault()
    e.stopPropagation()
    sendInput({ type: 'keydown', key: e.key, code: e.code })
  }, [sendInput])

  const onKeyUp = useCallback((e) => {
    if (e.key === 'Escape') return
    e.preventDefault()
    e.stopPropagation()
    sendInput({ type: 'keyup', key: e.key, code: e.code })
  }, [sendInput])

  useEffect(() => {
    if (status === 'connected') focusContainer()
  }, [status, focusContainer])

  // Auto-retry when browser service isn't running (e.g. after redeploy)
  useEffect(() => {
    if (!error) return
    const id = setInterval(() => connect(), 5000)
    return () => clearInterval(id)
  }, [error, connect])

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
    <div
      ref={containerRef}
      className="w-full h-full bg-neutral-950 outline-none overflow-hidden"
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
