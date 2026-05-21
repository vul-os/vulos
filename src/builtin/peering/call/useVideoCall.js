import { useState, useRef, useCallback, useEffect } from 'react'

// useVideoCall — thin extension layer composing useWebRTCCall (PEER-22).
//
// Adds to the base audio call:
//   • Video track via addTransceiver with 2-layer simulcast (high 720p / low 180p)
//   • Camera on/off toggle (replaceTrack with black-frame fallback)
//   • Screen sharing via getDisplayMedia with track swap into the existing transceiver
//   • Picture-in-picture for the remote video element
//   • Quality indicator polling (RTT, packet loss) via getStats
//
// USAGE:
//   import { useWebRTCCall } from './useWebRTCCall'
//   import { useVideoCall }  from './useVideoCall'
//
//   function CallScreen({ peerId, signalingWs }) {
//     const base   = useWebRTCCall({ peerId, signalingWs })
//     const video  = useVideoCall(base)
//     return <CallView base={base} video={video} />
//   }

// ---------------------------------------------------------------------------
// Simulcast encoding layers (PEERING.md §Call Features)
// High = 720p, Low = 180p — SFU picks which layer to forward per-receiver.
// ---------------------------------------------------------------------------
const SIMULCAST_ENCODINGS = [
  { rid: 'high', maxBitrate: 1_500_000, scaleResolutionDownBy: 1   }, // 720p
  { rid: 'low',  maxBitrate:   150_000, scaleResolutionDownBy: 4   }, // 180p
]

// ---------------------------------------------------------------------------
// Black-frame replacement track — sent when camera is muted so the transceiver
// stays alive and the SFU does not drop the sender slot.
// ---------------------------------------------------------------------------
function makeBlackFrameTrack() {
  const canvas = Object.assign(document.createElement('canvas'), { width: 2, height: 2 })
  canvas.getContext('2d').fillRect(0, 0, 2, 2)
  // 1 fps is enough — just a keep-alive signal.
  const stream = canvas.captureStream(1)
  return stream.getVideoTracks()[0]
}

// ---------------------------------------------------------------------------
// getStats helpers — extract RTT and inbound packet-loss from the peer conn.
// ---------------------------------------------------------------------------
async function pollQuality(pc) {
  if (!pc || pc.connectionState === 'closed') return null
  let rttMs    = null
  let lossRate = null

  try {
    const stats = await pc.getStats()
    stats.forEach((report) => {
      // Outbound RTP → candidatePair round-trip time (most reliable source)
      if (report.type === 'candidate-pair' && report.state === 'succeeded' && report.currentRoundTripTime != null) {
        rttMs = Math.round(report.currentRoundTripTime * 1000)
      }
      // Inbound video stream packet loss
      if (report.type === 'inbound-rtp' && report.kind === 'video') {
        const total    = (report.packetsReceived || 0) + (report.packetsLost || 0)
        lossRate = total > 0 ? (report.packetsLost || 0) / total : 0
      }
    })
  } catch {
    // getStats can throw if the connection is torn down mid-poll — ignore.
  }

  if (rttMs === null && lossRate === null) return null

  return {
    rttMs,
    lossPercent: lossRate != null ? Math.round(lossRate * 100 * 10) / 10 : null,
    // Derived quality label for UI badges
    quality: deriveQuality(rttMs, lossRate),
  }
}

function deriveQuality(rttMs, lossRate) {
  if (rttMs == null || lossRate == null) return 'unknown'
  if (rttMs < 80  && lossRate < 0.01)  return 'good'
  if (rttMs < 200 && lossRate < 0.05)  return 'fair'
  return 'poor'
}

// ---------------------------------------------------------------------------
// useVideoCall(base)
//
// base — the object returned by useWebRTCCall.  Expected shape (read-only):
//   base.peerConnectionRef  — React ref holding the RTCPeerConnection
//   base.connected          — boolean, true once ICE is settled
//   (all other fields from useWebRTCCall are passed through unchanged)
// ---------------------------------------------------------------------------
export function useVideoCall(base) {
  // --- state ----------------------------------------------------------------
  const [cameraOn,       setCameraOn]       = useState(false)
  const [screenSharing,  setScreenSharing]  = useState(false)
  const [pipActive,      setPipActive]      = useState(false)
  const [quality,        setQuality]        = useState(null)   // { rttMs, lossPercent, quality }
  const [videoError,     setVideoError]     = useState(null)

  // --- internal refs --------------------------------------------------------
  const localVideoRef        = useRef(null)   // <video> for local camera preview
  const remoteVideoRef       = useRef(null)   // <video> for remote stream — caller attaches
  const videoTransceiverRef  = useRef(null)   // RTCRtpTransceiver for the video track
  const cameraStreamRef      = useRef(null)   // MediaStream from getUserMedia (camera)
  const screenStreamRef      = useRef(null)   // MediaStream from getDisplayMedia
  const blackTrackRef        = useRef(null)   // Keep-alive when camera is off
  const qualityTimerRef      = useRef(null)

  // Convenience alias — lets the rest of the hook read the live peer connection.
  const pc = useCallback(
    () => base?.peerConnectionRef?.current ?? null,
    [base]
  )

  // ---------------------------------------------------------------------------
  // addVideoTransceiver — called once after the peer connection is created.
  //
  // Must be called BEFORE the SDP offer is generated (i.e. from the same
  // callsite that drives useWebRTCCall's connect flow, or inside a useEffect
  // that watches base.connected transitioning from false→true on the *caller*
  // side before the offer is sent).  For a 1-on-1 call the SFU layer is not
  // involved — simulcast is added anyway so the same hook works when promoted
  // to a group call without changes.
  // ---------------------------------------------------------------------------
  const addVideoTransceiver = useCallback(async () => {
    const conn = pc()
    if (!conn) return

    if (videoTransceiverRef.current) return  // idempotent

    // Start with a black frame so the transceiver is active immediately;
    // the user can turn the camera on separately.
    const silent = makeBlackFrameTrack()
    blackTrackRef.current = silent

    try {
      const transceiver = conn.addTransceiver(silent, {
        direction:  'sendrecv',
        sendEncodings: SIMULCAST_ENCODINGS,
        streams: [],
      })
      videoTransceiverRef.current = transceiver
    } catch (err) {
      setVideoError(`Failed to add video transceiver: ${err.message}`)
    }
  }, [pc])

  // ---------------------------------------------------------------------------
  // enableCamera — acquire getUserMedia, replace the sender track.
  // ---------------------------------------------------------------------------
  const enableCamera = useCallback(async (constraints = {}) => {
    setVideoError(null)
    const conn = pc()
    if (!conn) { setVideoError('No peer connection'); return }

    // Ensure the transceiver exists before we try to swap tracks.
    await addVideoTransceiver()

    try {
      // Stop any previous camera stream
      cameraStreamRef.current?.getTracks().forEach(t => t.stop())

      const stream = await navigator.mediaDevices.getUserMedia({
        video: { width: { ideal: 1280 }, height: { ideal: 720 }, ...constraints },
        audio: false,  // audio is handled by useWebRTCCall
      })
      cameraStreamRef.current = stream

      const [track] = stream.getVideoTracks()

      // Mirror to local preview element if connected
      if (localVideoRef.current) {
        localVideoRef.current.srcObject = stream
      }

      // Swap into the transceiver
      const sender = videoTransceiverRef.current?.sender
      if (sender) {
        await sender.replaceTrack(track)
      }

      // Keep-alive black track no longer needed
      blackTrackRef.current?.stop()
      blackTrackRef.current = null

      setCameraOn(true)
    } catch (err) {
      setVideoError(`Camera error: ${err.message}`)
    }
  }, [pc, addVideoTransceiver])

  // ---------------------------------------------------------------------------
  // disableCamera — swap camera track out for a silent black frame.
  // ---------------------------------------------------------------------------
  const disableCamera = useCallback(async () => {
    const sender = videoTransceiverRef.current?.sender
    if (!sender) return

    cameraStreamRef.current?.getTracks().forEach(t => t.stop())
    cameraStreamRef.current = null

    if (localVideoRef.current) {
      localVideoRef.current.srcObject = null
    }

    // Put the keep-alive black track back so the transceiver stays open.
    const silent = makeBlackFrameTrack()
    blackTrackRef.current = silent
    await sender.replaceTrack(silent)

    setCameraOn(false)
  }, [])

  // ---------------------------------------------------------------------------
  // toggleCamera — convenience wrapper.
  // ---------------------------------------------------------------------------
  const toggleCamera = useCallback(async () => {
    if (cameraOn) {
      await disableCamera()
    } else {
      await enableCamera()
    }
  }, [cameraOn, enableCamera, disableCamera])

  // ---------------------------------------------------------------------------
  // startScreenShare — getDisplayMedia → swap into the video transceiver.
  // Camera track is suspended while screen sharing; restored on stop.
  // ---------------------------------------------------------------------------
  const startScreenShare = useCallback(async () => {
    setVideoError(null)
    const conn = pc()
    if (!conn) { setVideoError('No peer connection'); return }

    await addVideoTransceiver()

    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({
        video: { cursor: 'always' },
        audio: false,
      })
      screenStreamRef.current = stream

      const [track] = stream.getVideoTracks()

      // When the user stops sharing via the browser's native UI, clean up.
      track.addEventListener('ended', () => stopScreenShare(), { once: true })

      const sender = videoTransceiverRef.current?.sender
      if (sender) {
        await sender.replaceTrack(track)
      }

      setScreenSharing(true)
    } catch (err) {
      if (err.name !== 'NotAllowedError') {
        // NotAllowedError means the user cancelled the picker — not a real error.
        setVideoError(`Screen share error: ${err.message}`)
      }
    }
  }, [pc, addVideoTransceiver])  // stopScreenShare added below via ref trick

  // ---------------------------------------------------------------------------
  // stopScreenShare — restore camera track (or black frame if camera was off).
  // ---------------------------------------------------------------------------
  const stopScreenShare = useCallback(async () => {
    screenStreamRef.current?.getTracks().forEach(t => t.stop())
    screenStreamRef.current = null

    const sender = videoTransceiverRef.current?.sender
    if (!sender) { setScreenSharing(false); return }

    if (cameraOn && cameraStreamRef.current) {
      // Camera was on — restore it.
      const [track] = cameraStreamRef.current.getVideoTracks()
      await sender.replaceTrack(track)
    } else {
      // Camera off — go back to black frame.
      const silent = makeBlackFrameTrack()
      blackTrackRef.current = silent
      await sender.replaceTrack(silent)
    }

    setScreenSharing(false)
  }, [cameraOn])

  // ---------------------------------------------------------------------------
  // toggleScreenShare — convenience wrapper.
  // ---------------------------------------------------------------------------
  const toggleScreenShare = useCallback(async () => {
    if (screenSharing) {
      await stopScreenShare()
    } else {
      await startScreenShare()
    }
  }, [screenSharing, startScreenShare, stopScreenShare])

  // ---------------------------------------------------------------------------
  // requestPip — enter Picture-in-Picture for the remote video element.
  // The caller must attach remoteVideoRef.current to their <video> element.
  // ---------------------------------------------------------------------------
  const requestPip = useCallback(async () => {
    const el = remoteVideoRef.current
    if (!el) { setVideoError('Remote video element not attached'); return }
    if (!document.pictureInPictureEnabled) { setVideoError('PiP not supported'); return }

    try {
      if (document.pictureInPictureElement) {
        await document.exitPictureInPicture()
        setPipActive(false)
      } else {
        await el.requestPictureInPicture()
        setPipActive(true)
      }
    } catch (err) {
      setVideoError(`PiP error: ${err.message}`)
    }
  }, [])

  // Sync pipActive with the browser's own PiP state (e.g. user closes PiP window).
  useEffect(() => {
    const el = remoteVideoRef.current
    if (!el) return

    const onEnter = () => setPipActive(true)
    const onLeave = () => setPipActive(false)
    el.addEventListener('enterpictureinpicture', onEnter)
    el.addEventListener('leavepictureinpicture', onLeave)
    return () => {
      el.removeEventListener('enterpictureinpicture', onEnter)
      el.removeEventListener('leavepictureinpicture', onLeave)
    }
  }, [])  // remoteVideoRef is stable

  // ---------------------------------------------------------------------------
  // Quality polling — start once the base call is connected, stop on teardown.
  // ---------------------------------------------------------------------------
  useEffect(() => {
    if (!base?.connected) {
      setQuality(null)
      return
    }

    const POLL_INTERVAL = 3000  // ms

    const tick = async () => {
      const conn = pc()
      if (!conn) return
      const q = await pollQuality(conn)
      if (q) setQuality(q)
    }

    tick()  // immediate first sample
    qualityTimerRef.current = setInterval(tick, POLL_INTERVAL)

    return () => {
      clearInterval(qualityTimerRef.current)
      qualityTimerRef.current = null
    }
  }, [base?.connected, pc])

  // ---------------------------------------------------------------------------
  // Cleanup on unmount — stop all media tracks.
  // ---------------------------------------------------------------------------
  useEffect(() => {
    return () => {
      cameraStreamRef.current?.getTracks().forEach(t => t.stop())
      screenStreamRef.current?.getTracks().forEach(t => t.stop())
      blackTrackRef.current?.stop()
      clearInterval(qualityTimerRef.current)

      // Exit PiP if active when the component unmounts.
      if (document.pictureInPictureElement) {
        document.exitPictureInPicture().catch(() => {})
      }
    }
  }, [])

  // ---------------------------------------------------------------------------
  // setRemoteVideoEl — callback ref for attaching the remote <video> element.
  // Keeps remoteVideoRef.current in sync without external mutation.
  // ---------------------------------------------------------------------------
  const setRemoteVideoEl = useCallback((el) => {
    remoteVideoRef.current = el
  }, [])  // remoteVideoRef is stable

  // ---------------------------------------------------------------------------
  // Public API
  // ---------------------------------------------------------------------------
  return {
    // --- refs (caller attaches to <video> elements) ---
    localVideoRef,   // local camera preview — use as ref on the <video> element
    remoteVideoRef,  // remote stream + PiP source

    // --- state ---
    cameraOn,
    screenSharing,
    pipActive,
    quality,         // null | { rttMs, lossPercent, quality: 'good'|'fair'|'poor'|'unknown' }
    videoError,

    // --- actions ---
    addVideoTransceiver,  // call before SDP offer if driving manually
    enableCamera,
    disableCamera,
    toggleCamera,
    startScreenShare,
    stopScreenShare,
    toggleScreenShare,
    requestPip,           // toggles PiP on the remote video element
    setRemoteVideoEl,     // callback ref — attach to the remote <video> element
  }
}
