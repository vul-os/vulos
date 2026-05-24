/**
 * BackgroundBlur.jsx — Apply a CSS/canvas blur to the local video track before publishing.
 *
 * Uses the MediaStreamTrack Insertable Streams (Breakout Box) API where available
 * (Chrome 94+, Edge 94+). Falls back to a canvas compositing approach.
 * Firefox: shows a graceful notice (no blur supported).
 *
 * Security note: the blur is applied client-side BEFORE the track is published to peers.
 * Nothing unblurred leaves the browser when blur is active. The "You look like this to
 * others" preview renders the *processed* track, not the raw camera.
 *
 * Props:
 *   call       — active call object (from createCall)
 *   enabled    — controlled boolean
 *   onToggle   — called with new boolean
 *   previewRef — optional ref to a <video> element showing the processed stream
 */
import { useEffect, useRef, useCallback } from 'react'
import { Tooltip } from '../../../components/ui'

const BLUR_PX = 12

// Detect support
function supportsInsertableStreams() {
  return (
    typeof MediaStreamTrackProcessor !== 'undefined' &&
    typeof MediaStreamTrackGenerator !== 'undefined'
  )
}

function supportsCanvas() {
  return typeof HTMLCanvasElement !== 'undefined'
}

function isFirefox() {
  return typeof navigator !== 'undefined' && /Firefox/i.test(navigator.userAgent)
}

/**
 * CanvasBlurProcessor — fallback canvas-based blur pipeline.
 * Draws the raw video frame to an offscreen canvas with `filter: blur(Npx)`,
 * then reads back as an ImageBitmap stream.
 */
class CanvasBlurProcessor {
  constructor(sourceTrack, blurPx = BLUR_PX) {
    this._source = sourceTrack
    this._blurPx = blurPx
    this._stopped = false
    this._canvas = document.createElement('canvas')
    this._ctx = this._canvas.getContext('2d')
    this._video = document.createElement('video')
    this._video.srcObject = new MediaStream([sourceTrack])
    this._video.autoplay = true
    this._video.playsInline = true
    this._video.muted = true
  }

  start() {
    const fps = 30
    const interval = 1000 / fps
    const draw = () => {
      if (this._stopped) return
      const { videoWidth: w, videoHeight: h } = this._video
      if (w && h) {
        if (this._canvas.width !== w) this._canvas.width = w
        if (this._canvas.height !== h) this._canvas.height = h
        this._ctx.filter = `blur(${this._blurPx}px)`
        this._ctx.drawImage(this._video, 0, 0, w, h)
        this._ctx.filter = 'none'
      }
      setTimeout(draw, interval)
    }
    this._video.onloadedmetadata = () => {
      this._video.play().catch(() => {})
      draw()
    }
    // Return a stream from the processed canvas
    return this._canvas.captureStream(fps)
  }

  stop() {
    this._stopped = true
    this._video.srcObject = null
  }
}

export default function BackgroundBlur({ call, enabled, onToggle, previewRef }) {
  const processorRef = useRef(null)

  useEffect(() => {
    if (!call) return
    const localTrack = call.localStream?.getVideoTracks()[0]
    if (!localTrack) return

    if (enabled) {
      if (supportsInsertableStreams()) {
        // Insertable Streams path
        try {
          const processor = new MediaStreamTrackProcessor({ track: localTrack })
          const generator = new MediaStreamTrackGenerator({ kind: 'video' })
          const offscreen = new OffscreenCanvas(1, 1)
          const ctx = offscreen.getContext('2d')
          const transformer = new TransformStream({
            transform(frame, controller) {
              if (offscreen.width !== frame.displayWidth) offscreen.width = frame.displayWidth
              if (offscreen.height !== frame.displayHeight) offscreen.height = frame.displayHeight
              ctx.filter = `blur(${BLUR_PX}px)`
              ctx.drawImage(frame, 0, 0)
              ctx.filter = 'none'
              const newFrame = new VideoFrame(offscreen, { timestamp: frame.timestamp })
              frame.close()
              controller.enqueue(newFrame)
            },
          })
          processor.readable.pipeThrough(transformer).pipeTo(generator.writable)
          const blurredStream = new MediaStream([generator])
          call.replaceVideoTrack(generator)
          if (previewRef?.current) previewRef.current.srcObject = blurredStream
          processorRef.current = { type: 'insertable', generator }
        } catch (e) {
          console.warn('[BackgroundBlur] insertable streams failed, fallback to canvas:', e)
        }
      }

      if (!processorRef.current && supportsCanvas()) {
        // Canvas fallback
        const proc = new CanvasBlurProcessor(localTrack, BLUR_PX)
        const blurredStream = proc.start()
        const blurredTrack = blurredStream?.getVideoTracks()[0]
        if (blurredTrack) {
          call.replaceVideoTrack(blurredTrack)
          if (previewRef?.current) previewRef.current.srcObject = blurredStream
        }
        processorRef.current = { type: 'canvas', proc }
      }
    } else {
      // Restore original track
      if (processorRef.current) {
        if (processorRef.current.type === 'canvas') {
          processorRef.current.proc.stop()
        } else if (processorRef.current.type === 'insertable') {
          try { processorRef.current.generator.stop() } catch (_) {}
        }
        call.replaceVideoTrack(localTrack)
        if (previewRef?.current) previewRef.current.srcObject = call.localStream
        processorRef.current = null
      }
    }

    // Cleanup: stop processor when the effect re-runs (call/enabled changed) or on unmount.
    return () => {
      if (processorRef.current) {
        if (processorRef.current.type === 'canvas') {
          processorRef.current.proc.stop()
        } else if (processorRef.current.type === 'insertable') {
          try { processorRef.current.generator.stop() } catch (_) {}
        }
        processorRef.current = null
      }
    }
  }, [call, enabled]) // previewRef intentionally excluded (ref is stable)

  const handleToggle = useCallback(() => {
    if (isFirefox()) {
      // Firefox does not support background processing — show user notice
      return
    }
    onToggle?.(!enabled)
  }, [enabled, onToggle])

  const ff = isFirefox()

  return (
    <Tooltip
      label={
        ff
          ? 'Background blur not supported in Firefox'
          : enabled
            ? 'Disable background blur'
            : 'Blur background'
      }
      side="top"
    >
      <button
        type="button"
        onClick={handleToggle}
        aria-label={enabled ? 'Disable background blur' : 'Blur background'}
        aria-pressed={enabled ? 'true' : 'false'}
        disabled={ff}
        className={[
          'inline-flex items-center justify-center w-10 h-10 rounded-md text-base',
          'transition-[background,color,opacity] duration-fast ease-out',
          'focus-visible:outline-none focus-visible:shadow-focus',
          ff
            ? 'opacity-40 cursor-not-allowed bg-paper/5 text-paper/40'
            : enabled
              ? 'bg-accent text-white hover:opacity-90'
              : 'bg-paper/10 text-paper hover:bg-paper/20',
        ].join(' ')}
      >
        {/* simple blur icon represented as SVG */}
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
          <circle cx="12" cy="12" r="3" />
          <path d="M12 2v2M12 20v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M2 12h2M20 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42" strokeOpacity=".5" />
        </svg>
      </button>
    </Tooltip>
  )
}
