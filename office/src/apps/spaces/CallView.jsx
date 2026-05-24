/**
 * CallView — Vulos Meet + Spaces call surface (Google Meet parity).
 *
 * Design pass:
 *   - Backdrop: warm ink (`bg-ink` paired with paper text) rather than slate.
 *   - Active speaker: quiet 2px accent outline (no garish emerald).
 *   - Transport badge (P2P vs Relay): tiny accent-tint pill (or warning when relay).
 *   - Controls: IconButtons in a dock-style cluster; leave is the only persimmon.
 *   - Screen-share area: kept at 55% height per spec.
 *   - Raise hand, reactions, background blur, presenter focus, captions, recording stub.
 *
 * Props:
 *   sessionId    — fabric session id for this call (channel id, DM id, room id)
 *   channelId    — Spaces channel/thread id for persisted in-call chat (OFFICE-66)
 *   threadParent — optional thread-parent message id for meeting-room threads
 *   identity     — { displayName, accountAddress, color }
 *   video        — start with camera on (default true)
 *   isOrganizer  — whether the viewer is the meeting organizer (enables spotlight-all, lobby admin)
 *   onLeave      — called after the call tears down
 */
import { useEffect, useRef, useState, useCallback } from 'react'
import {
  Mic, MicOff, Video, VideoOff, PhoneOff, Users,
  Wifi, WifiOff, MessageSquare, Monitor, MonitorOff,
} from 'lucide-react'
import { createCall } from '../../lib/call/rtc'
import InCallChat from './InCallChat.jsx'
import { Tooltip } from '../../components/ui'
import RaiseHand, { HandIndicator } from './components/RaiseHand.jsx'
import Reactions from './components/Reactions.jsx'
import BackgroundBlur from './components/BackgroundBlur.jsx'
import PresenterFocus, { usePinnedLayout } from './components/PresenterFocus.jsx'
import RecordingStub from './components/RecordingStub.jsx'
import Captions, { CaptionOverlay } from './components/Captions.jsx'

export default function CallView({
  sessionId, channelId, threadParent = '', identity, video = true, isOrganizer = false, onLeave,
}) {
  const [call, setCall] = useState(null)
  const [error, setError] = useState(null)
  const [muted, setMuted] = useState(false)
  const [cameraOff, setCameraOff] = useState(!video)
  const [peers, setPeers] = useState([])
  const [activeSpeaker, setActiveSpeaker] = useState(null)
  const [transport, setTransport] = useState('p2p')
  const [state, setState] = useState('connecting')
  const [showRoster, setShowRoster] = useState(false)
  const [showChat, setShowChat] = useState(false)
  const [screenSharing, setScreenSharing] = useState(false)
  const [screenPresenter, setScreenPresenter] = useState(null)
  // New Meet features
  const [handRaised, setHandRaised] = useState(false)
  const [peerHands, setPeerHands] = useState({}) // peerId → bool
  const [peerCaptions, setPeerCaptions] = useState({}) // peerId → string
  const [selfCaption, setSelfCaption] = useState('')
  const [blurEnabled, setBlurEnabled] = useState(false)
  const [captionsEnabled, setCaptionsEnabled] = useState(false)
  const [pinnedId, setPinnedId] = useState(null)
  const localVideoRef = useRef(null)
  const screenPreviewRef = useRef(null)
  const blurPreviewRef = useRef(null)

  useEffect(() => {
    let cancelled = false
    let activeCall = null
    ;(async () => {
      try {
        const c = await createCall({ sessionId, identity, video })
        if (cancelled) { c.leave(); return }
        activeCall = c
        setCall(c)
        setState(c.state)
        setMuted(c.muted)
        setCameraOff(c.cameraOff)
        setScreenSharing(c.screenSharing)
        if (localVideoRef.current) {
          localVideoRef.current.srcObject = c.localStream
        }
        c.on('peers-changed', (p) => setPeers([...p]))
        c.on('active-speaker', (id) => setActiveSpeaker(id))
        c.on('transport', (t) => setTransport(t))
        c.on('state', (s) => setState(s))
        c.on('screen-share', (peerId) => setScreenPresenter(peerId))
        // Raise-hand events from remote peers
        c.on('raise-hand', ({ peerId, raised }) => {
          setPeerHands((prev) => ({ ...prev, [peerId]: raised }))
        })
        // Remote captions
        c.on('caption', ({ peerId, text }) => {
          setPeerCaptions((prev) => ({ ...prev, [peerId]: text }))
          // Auto-clear after 4s
          setTimeout(() => {
            setPeerCaptions((prev) => {
              const next = { ...prev }
              if (next[peerId] === text) delete next[peerId]
              return next
            })
          }, 4000)
        })
        // Self-caption feedback
        c.on('caption-self', ({ text }) => {
          setSelfCaption(text)
          setTimeout(() => setSelfCaption((t) => (t === text ? '' : t)), 4000)
        })
        // Remote spotlight signal (organizer → everyone)
        c.on('spotlight', ({ peerId }) => setPinnedId(peerId))
      } catch (e) {
        console.error(e)
        if (!cancelled) setError(e.message || String(e))
      }
    })()
    return () => {
      cancelled = true
      if (activeCall) activeCall.leave()
    }
  }, [sessionId])

  useEffect(() => {
    if (call && localVideoRef.current && localVideoRef.current.srcObject !== call.localStream) {
      localVideoRef.current.srcObject = call.localStream
    }
  }, [call, peers.length])

  useEffect(() => {
    if (screenPreviewRef.current) {
      screenPreviewRef.current.srcObject =
        screenSharing && call?.screenStream ? call.screenStream : null
    }
  }, [screenSharing, call])

  const handleMute = useCallback(() => {
    if (!call) return
    setMuted(call.toggleMute())
  }, [call])

  const handleCamera = useCallback(() => {
    if (!call) return
    setCameraOff(call.toggleCamera())
  }, [call])

  const handleLeave = useCallback(() => {
    if (call) call.leave()
    onLeave?.()
  }, [call, onLeave])

  const handleScreenShare = useCallback(async () => {
    if (!call) return
    if (screenSharing) {
      call.stopScreenShare()
      setScreenSharing(false)
      setScreenPresenter(null)
    } else {
      try {
        await call.startScreenShare()
        setScreenSharing(true)
        setScreenPresenter('local')
      } catch (e) {
        console.warn('[screen-share] aborted:', e.message)
      }
    }
  }, [call, screenSharing])

  if (error) {
    return (
      <div
        className="flex flex-col items-center justify-center h-full p-8 text-paper"
        style={{ background: 'var(--ink)' }}
      >
        <div className="text-xl mb-1 font-serif">Couldn't start the call</div>
        <div className="text-sm text-paper/60 mb-6">{error}</div>
        <button
          type="button"
          onClick={handleLeave}
          className="px-4 h-8 rounded-md bg-danger text-white hover:opacity-90 text-sm font-medium tracking-tightish"
        >
          Close
        </button>
      </div>
    )
  }

  const totalTiles = peers.length + 1

  const presentingPeer = screenPresenter && screenPresenter !== 'local'
    ? peers.find((p) => p.peerId === screenPresenter) ?? null
    : null

  const anyScreenActive = screenSharing || !!presentingPeer

  // Presenter focus / pinned layout
  const { mainPeer, stripPeers, cols } = usePinnedLayout({
    peers,
    pinnedId,
    screenPresenter,
  })

  // Responsive grid: mobile 1-col, tablet 2-up, desktop up to 3x3
  const gridCols = mainPeer
    ? 1
    : totalTiles <= 1 ? 1 : totalTiles <= 4 ? 2 : 3

  return (
    <div
      className="flex flex-col h-full text-paper"
      style={{ background: 'var(--ink)' }}
    >
      <CallHeader
        state={state}
        transport={transport}
        participantCount={totalTiles}
        onToggleRoster={() => setShowRoster((v) => !v)}
      />

      <div className="flex-1 flex overflow-hidden">
        <div className="flex-1 flex flex-col overflow-hidden relative">
          {anyScreenActive && (
            <ScreenShareView
              isLocal={screenSharing && screenPresenter === 'local'}
              localRef={screenPreviewRef}
              presenterPeer={presentingPeer}
              presenterLabel={
                screenPresenter === 'local'
                  ? (identity?.displayName || 'You')
                  : (presentingPeer?.identity?.displayName || (presentingPeer?.peerId?.slice(0, 6) ?? 'Peer'))
              }
            />
          )}

          {/* Pinned main tile */}
          {mainPeer && !anyScreenActive && (
            <div
              className="mx-3 mt-3 rounded-lg overflow-hidden flex-shrink-0 relative"
              style={{ height: '55%', minHeight: 200 }}
            >
              <RemoteTile
                peer={mainPeer}
                isSpeaking={activeSpeaker === mainPeer.peerId}
                handRaised={peerHands[mainPeer.peerId]}
                captionText={peerCaptions[mainPeer.peerId]}
                onPin={() => setPinnedId(null)}
                isPinned
              />
            </div>
          )}

          <div
            className="flex-1 grid gap-2 p-3 overflow-auto"
            style={{ gridTemplateColumns: `repeat(${gridCols}, minmax(0, 1fr))` }}
          >
            <Tile
              label={identity?.displayName ? `${identity.displayName} (you)` : 'You'}
              muted={muted}
              cameraOff={cameraOff}
              isLocal
              videoRef={localVideoRef}
              color={identity?.color}
              isPresenting={screenPresenter === 'local'}
              handRaised={handRaised}
              captionText={selfCaption}
            />
            {(mainPeer ? stripPeers : peers).map((p) => (
              <RemoteTile
                key={p.peerId}
                peer={p}
                isSpeaking={activeSpeaker === p.peerId}
                handRaised={peerHands[p.peerId]}
                captionText={peerCaptions[p.peerId]}
                onPin={() => setPinnedId((cur) => cur === p.peerId ? null : p.peerId)}
              />
            ))}
          </div>

          {/* Floating reactions overlay — absolute over the tile grid */}
          <div className="absolute inset-0 pointer-events-none overflow-hidden">
            <Reactions call={call} />
          </div>
        </div>

        {showRoster && (
          <Roster
            peers={peers}
            self={identity}
            activeSpeaker={activeSpeaker}
            screenPresenter={screenPresenter}
            peerHands={peerHands}
          />
        )}
        {showChat && channelId && (
          <InCallChat
            channelId={channelId}
            threadParent={threadParent}
            identity={identity}
            onClose={() => setShowChat(false)}
          />
        )}
      </div>

      <Controls
        call={call}
        muted={muted}
        cameraOff={cameraOff}
        screenSharing={screenSharing}
        handRaised={handRaised}
        blurEnabled={blurEnabled}
        captionsEnabled={captionsEnabled}
        isOrganizer={isOrganizer}
        peers={peers}
        pinnedId={pinnedId}
        onMute={handleMute}
        onCamera={handleCamera}
        onScreenShare={handleScreenShare}
        onLeave={handleLeave}
        onToggleRoster={() => setShowRoster((v) => !v)}
        rosterActive={showRoster}
        onToggleChat={channelId ? () => setShowChat((v) => !v) : null}
        chatActive={showChat}
        onHandToggle={(raised) => setHandRaised(raised)}
        onBlurToggle={(on) => setBlurEnabled(on)}
        onCaptionsToggle={(on) => setCaptionsEnabled(on)}
        onPin={setPinnedId}
        identity={identity}
        blurPreviewRef={blurPreviewRef}
      />

      {/* Background blur processed-stream preview (hidden unless debugging) */}
      <BackgroundBlur
        call={call}
        enabled={blurEnabled}
        onToggle={setBlurEnabled}
        previewRef={blurPreviewRef}
      />
      <Captions
        call={call}
        enabled={captionsEnabled}
        onToggle={setCaptionsEnabled}
        identity={identity}
      />
    </div>
  )
}

// ScreenShareView — prominent area at 55% height
function ScreenShareView({ isLocal, localRef, presenterPeer, presenterLabel }) {
  const remoteRef = useRef(null)
  useEffect(() => {
    if (!isLocal && remoteRef.current && presenterPeer?.stream) {
      if (remoteRef.current.srcObject !== presenterPeer.stream) {
        remoteRef.current.srcObject = presenterPeer.stream
      }
    }
  }, [isLocal, presenterPeer?.stream])

  return (
    <div
      className="relative mx-3 mt-3 rounded-lg overflow-hidden flex-shrink-0 border border-paper/10"
      style={{ height: '55%', minHeight: 200, background: '#000' }}
    >
      {isLocal ? (
        <video ref={localRef} autoPlay playsInline muted className="w-full h-full object-contain" />
      ) : (
        <video ref={remoteRef} autoPlay playsInline className="w-full h-full object-contain" />
      )}
      <div
        className="absolute top-2 left-3 flex items-center gap-1.5 px-2 py-1 rounded-pill text-2xs text-paper tracking-tightish"
        style={{ background: 'rgba(26,25,22,.6)' }}
      >
        <Monitor size={11} className="text-accent" />
        <span>{presenterLabel} is presenting</span>
        {isLocal && (
          <span className="ml-1 bg-accent text-white px-1.5 py-0.5 rounded-xs text-[9px] font-semibold uppercase tracking-eyebrow">
            You
          </span>
        )}
      </div>
    </div>
  )
}

function CallHeader({ state, transport, participantCount, onToggleRoster }) {
  const stateLabel =
    state === 'connecting' ? 'Connecting…' :
    state === 'connected' ? 'Connected' :
    state === 'closed' ? 'Call ended' : state

  const relay = transport === 'relay'
  return (
    <div className="px-4 h-11 flex items-center gap-3 text-xs border-b border-paper/10">
      <span className="text-paper/80 tracking-tightish">{stateLabel}</span>
      <span
        className={[
          'inline-flex items-center gap-1.5 px-2 py-0.5 rounded-pill text-2xs font-medium tracking-tightish',
          relay
            ? 'bg-warning/15 text-warning border border-warning/30'
            : 'bg-accent/15 text-accent border border-accent/30',
        ].join(' ')}
      >
        {relay ? <WifiOff size={11} /> : <Wifi size={11} />}
        <span>{relay ? 'Relay (TURN)' : 'P2P'}</span>
      </span>
      <button
        type="button"
        onClick={onToggleRoster}
        className="ml-auto inline-flex items-center gap-1 text-paper/70 hover:text-paper transition-colors duration-fast"
        title="Participants"
      >
        <Users size={13} />
        <span className="tracking-tightish">{participantCount}</span>
      </button>
    </div>
  )
}

function Tile({ label, muted, cameraOff, isLocal, videoRef, color, isPresenting, handRaised, captionText }) {
  return (
    <div
      className="relative rounded-lg overflow-hidden flex items-center justify-center min-h-[140px]"
      style={{
        background: 'rgba(255,255,255,.04)',
        outline: isPresenting
          ? '2px solid var(--accent)'
          : color
            ? `2px solid ${color}`
            : '1px solid rgba(255,255,255,.06)',
        outlineOffset: '-2px',
      }}
    >
      {!cameraOff && (
        <video
          ref={videoRef}
          autoPlay
          playsInline
          muted={isLocal}
          className="w-full h-full object-cover"
        />
      )}
      {cameraOff && (
        <div className="text-paper/40 text-3xl uppercase font-semibold tracking-tightish">
          {(label || '?').slice(0, 1)}
        </div>
      )}
      {/* Captions overlay */}
      {captionText && (
        <div className="absolute bottom-8 left-2 right-2 flex justify-center pointer-events-none">
          <span
            className="max-w-full px-2 py-1 rounded-sm text-2xs text-paper text-center leading-snug tracking-tightish"
            style={{ background: 'rgba(26,25,22,.78)' }}
            aria-live="polite"
          >
            {captionText}
          </span>
        </div>
      )}
      <div className="absolute bottom-1.5 left-2 right-2 flex items-center justify-between text-2xs text-paper">
        <span
          className="inline-flex items-center gap-1 px-2 py-0.5 rounded-pill tracking-tightish"
          style={{ background: 'rgba(26,25,22,.55)' }}
        >
          {isPresenting && <Monitor size={10} className="text-accent" />}
          {label}
        </span>
        <span className="inline-flex items-center gap-1">
          {handRaised && (
            <span
              className="inline-flex items-center justify-center w-5 h-5 rounded-pill bg-warning/90 text-white"
              title="Hand raised"
            >
              ✋
            </span>
          )}
          {muted && (
            <span
              className="inline-flex items-center justify-center w-5 h-5 rounded-pill"
              style={{ background: 'rgba(26,25,22,.55)' }}
              title="Muted"
            >
              <MicOff size={11} />
            </span>
          )}
        </span>
      </div>
    </div>
  )
}

function RemoteTile({ peer, isSpeaking, handRaised, captionText, onPin, isPinned }) {
  const ref = useRef(null)
  useEffect(() => {
    if (ref.current && peer.stream && ref.current.srcObject !== peer.stream) {
      ref.current.srcObject = peer.stream
    }
  }, [peer.stream])
  const label = peer.identity?.displayName || peer.peerId.slice(0, 6)
  const noVideo = !peer.stream || peer.stream.getVideoTracks().every((t) => !t.enabled)
  return (
    <div
      className="relative rounded-lg overflow-hidden flex items-center justify-center min-h-[140px] transition-[outline] duration-fast ease-out group"
      style={{
        background: 'rgba(255,255,255,.04)',
        outline: isPinned
          ? '2px solid var(--warning)'
          : peer.isPresenting || isSpeaking
            ? '2px solid var(--accent)'
            : '1px solid rgba(255,255,255,.06)',
        outlineOffset: '-2px',
      }}
    >
      <video ref={ref} autoPlay playsInline className="w-full h-full object-cover" />
      {noVideo && (
        <div className="absolute inset-0 flex items-center justify-center text-paper/40 text-3xl uppercase font-semibold tracking-tightish">
          {label.slice(0, 1)}
        </div>
      )}
      {/* Caption overlay */}
      {captionText && (
        <div className="absolute bottom-8 left-2 right-2 flex justify-center pointer-events-none">
          <span
            className="max-w-full px-2 py-1 rounded-sm text-2xs text-paper text-center leading-snug tracking-tightish"
            style={{ background: 'rgba(26,25,22,.78)' }}
            aria-live="polite"
          >
            {captionText}
          </span>
        </div>
      )}
      {/* Pin button — visible on hover */}
      {onPin && (
        <button
          type="button"
          onClick={onPin}
          className="absolute top-1.5 right-1.5 opacity-0 group-hover:opacity-100 transition-opacity duration-fast inline-flex items-center justify-center w-6 h-6 rounded-sm bg-paper/20 text-paper hover:bg-paper/35"
          title={isPinned ? 'Unpin' : 'Pin presenter'}
          aria-label={isPinned ? 'Unpin' : 'Pin presenter'}
        >
          📌
        </button>
      )}
      <div className="absolute bottom-1.5 left-2 right-2 flex items-center justify-between text-2xs">
        <span
          className="inline-flex items-center gap-1 px-2 py-0.5 rounded-pill tracking-tightish text-paper"
          style={{ background: 'rgba(26,25,22,.55)' }}
        >
          {peer.isPresenting && <Monitor size={10} className="text-accent" />}
          {label}
        </span>
        <span className="inline-flex items-center gap-1">
          {handRaised && (
            <span
              className="inline-flex items-center justify-center w-5 h-5 rounded-pill bg-warning/90 text-white text-[10px]"
              title="Hand raised"
            >
              ✋
            </span>
          )}
          {peer.usingRelay && (
            <span
              className="inline-flex items-center px-2 py-0.5 rounded-pill text-[10px] font-medium uppercase tracking-eyebrow"
              style={{
                background: 'rgba(192,132,54,.18)',
                color: 'var(--signal-warning)',
                border: '1px solid rgba(192,132,54,.35)',
              }}
            >
              relay
            </span>
          )}
        </span>
      </div>
    </div>
  )
}

function Roster({ peers, self, activeSpeaker, screenPresenter, peerHands = {} }) {
  // Sort: hands raised first
  const sorted = [...peers].sort((a, b) => {
    const aHand = peerHands[a.peerId] ? 1 : 0
    const bHand = peerHands[b.peerId] ? 1 : 0
    return bHand - aHand
  })
  return (
    <aside className="w-60 border-l border-paper/10 overflow-y-auto p-3 text-sm">
      <h3 className="text-2xs uppercase text-paper/50 mb-2 tracking-eyebrow font-semibold">
        Participants ({peers.length + 1})
      </h3>
      <ul className="space-y-1">
        <li className="flex items-center justify-between py-1 text-paper/90">
          <span className="inline-flex items-center gap-1.5 tracking-tightish">
            {screenPresenter === 'local' && <Monitor size={12} className="text-accent" />}
            <span className="font-serif italic">{self?.displayName || 'You'}</span>
            <span className="text-paper/40 text-2xs">(you)</span>
          </span>
        </li>
        {sorted.map((p) => (
          <li key={p.peerId} className="flex items-center justify-between py-1">
            <span
              className={[
                'inline-flex items-center gap-1.5 tracking-tightish',
                activeSpeaker === p.peerId ? 'text-accent' : 'text-paper/85',
              ].join(' ')}
            >
              {p.isPresenting && <Monitor size={12} className="text-accent" />}
              {peerHands[p.peerId] && <span title="Hand raised" className="text-[12px]">✋</span>}
              <span className="font-serif italic">
                {p.identity?.displayName || p.peerId.slice(0, 6)}
              </span>
            </span>
            <span className="text-[10px] text-paper/40 uppercase tracking-eyebrow">
              {p.usingRelay ? 'relay' : p.state}
            </span>
          </li>
        ))}
      </ul>
    </aside>
  )
}

// DockButton — IconButton-style affordance, themed for the dark call surface.
function DockButton({ onClick, active, title, children }) {
  return (
    <Tooltip label={title} side="top">
      <button
        type="button"
        onClick={onClick}
        aria-label={title}
        aria-pressed={active ? 'true' : 'false'}
        className={[
          'inline-flex items-center justify-center w-10 h-10 rounded-md',
          'transition-[background,color] duration-fast ease-out',
          'focus-visible:outline-none focus-visible:shadow-focus',
          active
            ? 'bg-accent text-white hover:bg-accent-hover'
            : 'bg-paper/10 text-paper hover:bg-paper/20',
        ].join(' ')}
      >
        {children}
      </button>
    </Tooltip>
  )
}

function Controls({
  call,
  muted, cameraOff, screenSharing,
  handRaised, blurEnabled, captionsEnabled,
  isOrganizer, peers, pinnedId,
  onMute, onCamera, onScreenShare, onLeave,
  onToggleRoster, rosterActive,
  onToggleChat, chatActive,
  onHandToggle, onBlurToggle, onCaptionsToggle, onPin,
  identity, blurPreviewRef,
}) {
  return (
    <div className="px-4 py-3 border-t border-paper/10 flex items-center justify-center relative">
      {/* Reactions popover anchors here */}
      <div
        className="inline-flex items-center gap-2 px-2 py-1.5 rounded-lg border border-paper/10 relative"
        style={{ background: 'rgba(255,255,255,.04)' }}
      >
        <DockButton onClick={onMute} active={muted} title={muted ? 'Unmute' : 'Mute'}>
          {muted ? <MicOff size={17} /> : <Mic size={17} />}
        </DockButton>
        <DockButton onClick={onCamera} active={cameraOff} title={cameraOff ? 'Camera on' : 'Camera off'}>
          {cameraOff ? <VideoOff size={17} /> : <Video size={17} />}
        </DockButton>
        <DockButton onClick={onScreenShare} active={screenSharing} title={screenSharing ? 'Stop sharing' : 'Share screen'}>
          {screenSharing ? <MonitorOff size={17} /> : <Monitor size={17} />}
        </DockButton>

        <span className="w-px h-6 bg-paper/10 mx-1" aria-hidden />

        {/* Raise hand */}
        <RaiseHand
          call={call}
          raised={handRaised}
          onToggle={onHandToggle}
          identity={identity}
        />

        {/* Floating reactions — palette pops above the dock */}
        <Reactions call={call} />

        <span className="w-px h-6 bg-paper/10 mx-1" aria-hidden />

        {/* Background blur */}
        <BackgroundBlur
          call={call}
          enabled={blurEnabled}
          onToggle={onBlurToggle}
          previewRef={blurPreviewRef}
        />

        {/* Captions */}
        <Captions
          call={call}
          enabled={captionsEnabled}
          onToggle={onCaptionsToggle}
          identity={identity}
        />

        {/* Recording stub */}
        <RecordingStub />

        <span className="w-px h-6 bg-paper/10 mx-1" aria-hidden />

        <DockButton onClick={onToggleRoster} active={rosterActive} title="Participants">
          <Users size={17} />
        </DockButton>
        {onToggleChat && (
          <DockButton onClick={onToggleChat} active={chatActive} title="In-call chat">
            <MessageSquare size={17} />
          </DockButton>
        )}
        <span className="w-px h-6 bg-paper/10 mx-1" aria-hidden />
        <Tooltip label="Leave call" side="top">
          <button
            type="button"
            onClick={onLeave}
            className="inline-flex items-center gap-2 h-10 px-4 rounded-md bg-danger text-white hover:opacity-90 text-sm font-medium tracking-tightish transition-opacity duration-fast focus-visible:outline-none focus-visible:shadow-focus"
          >
            <PhoneOff size={16} />
            <span>Leave</span>
          </button>
        </Tooltip>
      </div>
    </div>
  )
}
