// Room.jsx — OFFICE-65: Meeting room join/lobby + per-room call surface.
//
// Route: /room/:sessionId
//   :sessionId is the fabric session id (e.g. "meeting:<id>") — the same
//   value that createCall() receives.  CallView handles the WebRTC mesh.
//
// Design pass: warm paper lobby, serif headline ("Ready to join?"), camera
// preview framed warmly via Card, invitee roster as warm-tint Card pills,
// single accent join button.

import { useEffect, useState, useCallback, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  Video, Mic, MicOff, VideoOff, ArrowLeft, Users, Calendar,
} from 'lucide-react'
import CallView from './CallView'
import { Button, Card, Input, Tooltip } from '../../components/ui'

// Attempt to fetch meeting metadata (title, invitees, etc).  Non-blocking.
async function fetchMeetingMeta(sessionId) {
  const id = sessionId.startsWith('meeting:') ? sessionId.slice('meeting:'.length) : null
  if (!id) return null
  try {
    const r = await fetch(`/api/meetings/${encodeURIComponent(id)}/join`, {
      credentials: 'include',
    })
    if (r.ok) return r.json()
  } catch {}
  return null
}

// Warm presence-tint palette — no Tailwind defaults.
function pickColor() {
  const palette = ['#0f6a6c', '#4f7a4d', '#c08436', '#b8453a', '#4a6b8a', '#6e5b8a']
  return palette[Math.floor(Math.random() * palette.length)]
}

export default function Room() {
  const { sessionId: rawSessionId } = useParams()
  const sessionId = decodeURIComponent(rawSessionId || '')
  const navigate = useNavigate()

  const [phase, setPhase] = useState('lobby')  // 'lobby' | 'call' | 'ended'
  const [displayName, setDisplayName] = useState('')
  const [accountAddress, setAccountAddress] = useState('')
  const [videoOn, setVideoOn] = useState(true)
  const [micOn, setMicOn] = useState(true)
  const [meta, setMeta] = useState(null)
  const [identity, setIdentity] = useState(null)
  const previewRef = useRef(null)
  const previewStreamRef = useRef(null)

  useEffect(() => {
    let cancelled = false
    fetchMeetingMeta(sessionId).then((m) => {
      if (!cancelled) setMeta(m)
    })
    return () => { cancelled = true }
  }, [sessionId])

  // Local camera preview for the lobby — released when we leave the lobby.
  useEffect(() => {
    if (phase !== 'lobby' || !videoOn) {
      const s = previewStreamRef.current
      if (s) { s.getTracks().forEach((t) => t.stop()); previewStreamRef.current = null }
      if (previewRef.current) previewRef.current.srcObject = null
      return
    }
    let cancelled = false
    navigator.mediaDevices
      .getUserMedia({ video: true, audio: false })
      .then((stream) => {
        if (cancelled) { stream.getTracks().forEach((t) => t.stop()); return }
        previewStreamRef.current = stream
        if (previewRef.current) previewRef.current.srcObject = stream
      })
      .catch(() => { /* preview is best-effort */ })
    return () => {
      cancelled = true
      const s = previewStreamRef.current
      if (s) { s.getTracks().forEach((t) => t.stop()); previewStreamRef.current = null }
    }
  }, [phase, videoOn])

  const handleJoin = useCallback(() => {
    if (!displayName.trim()) return
    const id = {
      displayName: displayName.trim(),
      accountAddress: accountAddress.trim() || null,
      color: pickColor(),
      peerId: null,
    }
    setIdentity(id)
    setPhase('call')
  }, [displayName, accountAddress])

  const handleLeave = useCallback(() => { setPhase('ended') }, [])
  const handleBack = useCallback(() => { navigate('/meetings') }, [navigate])

  const meetingTitle = meta?.meeting?.title || sessionId

  if (phase === 'ended') {
    return (
      <div className="flex flex-col items-center justify-center h-screen bg-bg text-ink gap-4 px-4">
        <h2 className="text-2xl font-serif tracking-tightish">You have left the meeting.</h2>
        <p className="text-sm text-ink-muted">{meetingTitle}</p>
        <Button variant="primary" size="lg" onClick={handleBack} className="mt-2">
          Back to meetings
        </Button>
      </div>
    )
  }

  if (phase === 'call') {
    return (
      <div className="flex flex-col h-screen" style={{ background: 'var(--ink)' }}>
        <div className="flex-shrink-0 px-4 h-11 flex items-center gap-3 text-paper text-sm border-b border-paper/10">
          <Tooltip label="Back to meetings" side="bottom">
            <button
              type="button"
              onClick={handleBack}
              className="inline-flex items-center justify-center w-7 h-7 rounded-sm text-paper/70 hover:text-paper hover:bg-paper/10 transition-colors duration-fast"
              aria-label="Back to meetings"
            >
              <ArrowLeft size={15} />
            </button>
          </Tooltip>
          <span className="font-medium truncate tracking-tightish">{meetingTitle}</span>
          {meta?.meeting?.invitees?.length > 0 && (
            <span className="ml-auto inline-flex items-center gap-1 text-2xs text-paper/60 tracking-tightish">
              <Users size={11} />
              {meta.meeting.invitees.length} invited
            </span>
          )}
        </div>

        <div className="flex-1 min-h-0">
          <CallView
            sessionId={sessionId}
            identity={identity}
            video={videoOn}
            onLeave={handleLeave}
          />
        </div>
      </div>
    )
  }

  // ── Lobby ────────────────────────────────────────────────────────────────
  return (
    <div className="flex flex-col items-center justify-center min-h-screen bg-bg text-ink px-4 py-10">
      <Card variant="raised" className="w-full max-w-md">
        <Card.Body className="space-y-5">
          {/* Headline */}
          <div className="text-center space-y-1.5">
            <p className="text-2xs text-ink-faint uppercase tracking-eyebrow font-semibold">
              Meeting room
            </p>
            <h1 className="text-2xl font-serif tracking-tightish text-ink">
              {meetingTitle}
            </h1>
            {(meta?.meeting?.host_vumail || meta?.meeting?.scheduled_at) && (
              <p className="text-2xs text-ink-muted">
                {meta?.meeting?.host_vumail && <>Hosted by {meta.meeting.host_vumail}</>}
                {meta?.meeting?.host_vumail && meta?.meeting?.scheduled_at && ' · '}
                {meta?.meeting?.scheduled_at && (
                  <span className="inline-flex items-center gap-1">
                    <Calendar size={10} className="text-ink-faint" />
                    {new Date(meta.meeting.scheduled_at).toLocaleString()}
                  </span>
                )}
              </p>
            )}
          </div>

          {/* Camera preview frame — warm paper border, calm shadow */}
          <div
            className="relative rounded-md overflow-hidden border border-line-strong bg-clay shadow-e1"
            style={{ aspectRatio: '16 / 10' }}
          >
            {videoOn ? (
              <video
                ref={previewRef}
                autoPlay
                playsInline
                muted
                className="w-full h-full object-cover"
              />
            ) : (
              <div className="absolute inset-0 flex items-center justify-center">
                <div className="flex flex-col items-center gap-2 text-ink-faint">
                  <VideoOff size={28} />
                  <span className="text-xs font-serif italic">Camera off</span>
                </div>
              </div>
            )}
            <div className="absolute bottom-2 left-2 right-2 flex items-center justify-center gap-2">
              <Tooltip label={micOn ? 'Mute microphone' : 'Unmute microphone'} side="top">
                <button
                  type="button"
                  onClick={() => setMicOn((v) => !v)}
                  aria-label={micOn ? 'Mute microphone' : 'Unmute microphone'}
                  className={[
                    'inline-flex items-center justify-center w-9 h-9 rounded-pill',
                    'transition-[background,color] duration-fast ease-out',
                    micOn
                      ? 'bg-paper/85 text-ink hover:bg-paper'
                      : 'bg-danger text-white hover:opacity-90',
                  ].join(' ')}
                >
                  {micOn ? <Mic size={15} /> : <MicOff size={15} />}
                </button>
              </Tooltip>
              <Tooltip label={videoOn ? 'Turn off camera' : 'Turn on camera'} side="top">
                <button
                  type="button"
                  onClick={() => setVideoOn((v) => !v)}
                  aria-label={videoOn ? 'Turn off camera' : 'Turn on camera'}
                  className={[
                    'inline-flex items-center justify-center w-9 h-9 rounded-pill',
                    'transition-[background,color] duration-fast ease-out',
                    videoOn
                      ? 'bg-paper/85 text-ink hover:bg-paper'
                      : 'bg-danger text-white hover:opacity-90',
                  ].join(' ')}
                >
                  {videoOn ? <Video size={15} /> : <VideoOff size={15} />}
                </button>
              </Tooltip>
            </div>
          </div>

          {/* Invitee roster */}
          {meta?.meeting?.invitees?.length > 0 && (
            <div className="space-y-1.5">
              <p className="text-2xs text-ink-faint uppercase tracking-eyebrow font-semibold">
                Invited
              </p>
              <div className="flex flex-wrap gap-1.5">
                {meta.meeting.invitees.slice(0, 6).map((inv) => (
                  <span
                    key={inv}
                    className="inline-flex items-center gap-1 bg-accent-tint border border-line rounded-pill px-2 py-0.5 text-2xs text-ink-muted tracking-tightish"
                  >
                    {inv}
                  </span>
                ))}
                {meta.meeting.invitees.length > 6 && (
                  <span className="text-2xs text-ink-faint px-1 self-center">
                    +{meta.meeting.invitees.length - 6} more
                  </span>
                )}
              </div>
            </div>
          )}

          {/* Identity form */}
          <div className="space-y-3">
            <Input
              label="Display name"
              placeholder="Your name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleJoin()}
              autoFocus
            />
            <Input
              label="Vulos account (optional)"
              placeholder="you@vulos"
              value={accountAddress}
              onChange={(e) => setAccountAddress(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleJoin()}
            />
          </div>

          <Button
            variant="primary"
            size="lg"
            fullWidth
            disabled={!displayName.trim()}
            onClick={handleJoin}
          >
            Join now
          </Button>

          <button
            type="button"
            onClick={handleBack}
            className="w-full inline-flex items-center justify-center gap-1 text-2xs text-ink-faint hover:text-ink-muted transition-colors"
          >
            <ArrowLeft size={11} />
            Back to meetings
          </button>
        </Card.Body>
      </Card>
    </div>
  )
}
