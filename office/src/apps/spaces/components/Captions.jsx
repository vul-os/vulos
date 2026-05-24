/**
 * Captions.jsx — Best-effort live captions via browser Speech Recognition.
 *
 * Security: speech recognition runs on-device (webkitSpeechRecognition / SpeechRecognition
 * browser API). No audio is sent to any external transcription service.
 *
 * Your own captions are broadcast to peers via the WebRTC data channel.
 * Peer captions are overlaid on their video tile.
 *
 * Firefox: graceful "not supported" notice.
 * Chrome/Safari: full transcription support.
 *
 * Props:
 *   call       — active call object
 *   enabled    — controlled boolean
 *   onToggle   — called with new boolean
 *   identity   — { displayName } for the caption label
 */
import { useEffect, useRef, useCallback } from 'react'
import DOMPurify from 'dompurify'
import { Captions as CaptionsIcon, CaptionsOff } from 'lucide-react'
import { Tooltip } from '../../../components/ui'

/**
 * sanitizeCaption strips any HTML/script content from a caption string before
 * rendering it. React's JSX interpolation already auto-escapes, so this is a
 * defence-in-depth measure against future code changes that might introduce
 * dangerouslySetInnerHTML. Returns a plain-text string.
 */
function sanitizeCaption(text) {
  if (typeof text !== 'string') return ''
  return DOMPurify.sanitize(text, { ALLOWED_TAGS: [], ALLOWED_ATTR: [] })
}

function getSpeechRecognition() {
  return (
    (typeof window !== 'undefined' && (window.SpeechRecognition || window.webkitSpeechRecognition))
    || null
  )
}

export function isCaptionsSupported() {
  return Boolean(getSpeechRecognition())
}

export default function Captions({ call, enabled, onToggle, identity }) {
  const recognitionRef = useRef(null)
  const supported = isCaptionsSupported()

  useEffect(() => {
    if (!call) return
    // Receive captions from peers via data channel
    const handler = ({ peerId, text }) => {
      call.emit('caption-received', { peerId, text })
    }
    call.on('caption', handler)
    return () => call.off('caption', handler)
  }, [call])

  useEffect(() => {
    if (!supported || !enabled || !call) {
      recognitionRef.current?.stop()
      recognitionRef.current = null
      return
    }

    const SR = getSpeechRecognition()
    const recognition = new SR()
    recognition.continuous = true
    recognition.interimResults = true
    recognition.lang = 'en-US'

    recognition.onresult = (event) => {
      let interim = ''
      for (let i = event.resultIndex; i < event.results.length; i++) {
        const transcript = event.results[i][0].transcript
        if (event.results[i].isFinal) {
          call.sendDataChannelMsg({ type: 'caption', text: transcript.trim() })
        } else {
          interim += transcript
        }
        // Emit locally too so the self-tile can show it
        call.emit('caption-self', { text: event.results[i].isFinal ? transcript.trim() : interim })
      }
    }

    recognition.onerror = (e) => {
      if (e.error === 'aborted' || e.error === 'no-speech') return
      console.warn('[Captions] recognition error:', e.error)
    }

    recognition.onend = () => {
      // Auto-restart while still enabled
      if (enabled && recognitionRef.current === recognition) {
        try { recognition.start() } catch (_) {}
      }
    }

    try {
      recognition.start()
    } catch (_) {}

    recognitionRef.current = recognition
    return () => {
      recognition.onend = null
      recognition.stop()
      recognitionRef.current = null
    }
  }, [call, enabled, supported])

  const handleToggle = useCallback(() => {
    if (!supported) return
    onToggle?.(!enabled)
  }, [supported, enabled, onToggle])

  if (!supported) {
    return (
      <Tooltip label="Live captions not supported in this browser" side="top">
        <button
          type="button"
          disabled
          aria-label="Captions (not supported)"
          className="inline-flex items-center justify-center w-10 h-10 rounded-md opacity-35 cursor-not-allowed bg-paper/5 text-paper/40 border border-paper/10"
        >
          <CaptionsOff size={17} />
        </button>
      </Tooltip>
    )
  }

  return (
    <Tooltip label={enabled ? 'Turn off captions' : 'Turn on captions'} side="top">
      <button
        type="button"
        onClick={handleToggle}
        aria-label={enabled ? 'Turn off captions' : 'Turn on captions'}
        aria-pressed={enabled ? 'true' : 'false'}
        className={[
          'inline-flex items-center justify-center w-10 h-10 rounded-md',
          'transition-[background,color] duration-fast ease-out',
          'focus-visible:outline-none focus-visible:shadow-focus',
          enabled
            ? 'bg-accent text-white hover:opacity-90'
            : 'bg-paper/10 text-paper hover:bg-paper/20',
        ].join(' ')}
      >
        {enabled ? <CaptionsIcon size={17} /> : <CaptionsOff size={17} />}
      </button>
    </Tooltip>
  )
}

/**
 * CaptionOverlay — shown at the bottom of a video tile when the peer is speaking.
 * Receives the latest caption text for a given peer id.
 */
export function CaptionOverlay({ text }) {
  // sanitizeCaption strips HTML tags — defence in depth even though React
  // auto-escapes JSX interpolation. Never use dangerouslySetInnerHTML here.
  const safeText = sanitizeCaption(text)
  if (!safeText) return null
  return (
    <div
      className="absolute bottom-8 left-2 right-2 flex justify-center pointer-events-none"
      aria-live="polite"
      aria-atomic="true"
    >
      <span
        className="max-w-full px-2 py-1 rounded-sm text-2xs text-paper text-center leading-snug tracking-tightish"
        style={{ background: 'rgba(26,25,22,.78)', backdropFilter: 'blur(2px)' }}
      >
        {safeText}
      </span>
    </div>
  )
}
