import { useState, useRef, useCallback } from 'react'

// The Web Speech API's recognizer object (SpeechRecognition/webkitSpeechRecognition)
// isn't in TS's lib.dom.d.ts — only its supporting event/result interfaces are
// (SpeechRecognitionEvent, SpeechRecognitionErrorEvent, SpeechRecognitionResultList,
// …). This declares the minimal real shape this hook actually uses, and augments
// Window with the two vendor-prefixed constructor slots.
interface SpeechRecognizer extends EventTarget {
  continuous: boolean
  interimResults: boolean
  lang: string
  start(): void
  stop(): void
  onstart: ((this: SpeechRecognizer, ev: Event) => void) | null
  onresult: ((this: SpeechRecognizer, ev: SpeechRecognitionEvent) => void) | null
  onerror: ((this: SpeechRecognizer, ev: SpeechRecognitionErrorEvent) => void) | null
  onend: ((this: SpeechRecognizer, ev: Event) => void) | null
}

interface SpeechRecognizerConstructor {
  new (): SpeechRecognizer
}

declare global {
  interface Window {
    SpeechRecognition?: SpeechRecognizerConstructor
    webkitSpeechRecognition?: SpeechRecognizerConstructor
  }
}

const SpeechRecognitionCtor: SpeechRecognizerConstructor | null = typeof window !== 'undefined'
  ? (window.SpeechRecognition || window.webkitSpeechRecognition || null)
  : null

interface UseVoiceResult {
  listening: boolean
  supported: boolean
  start: () => void
  stop: () => void
}

export function useVoice(onResult?: (transcript: string) => void): UseVoiceResult {
  const [listening, setListening] = useState(false)
  const recognitionRef = useRef<SpeechRecognizer | null>(null)

  const supported = !!SpeechRecognitionCtor

  const start = useCallback(() => {
    if (!SpeechRecognitionCtor || listening) return

    const recognition = new SpeechRecognitionCtor()
    recognition.continuous = false
    recognition.interimResults = false
    recognition.lang = navigator.language || 'en-US'

    recognition.onstart = () => setListening(true)

    recognition.onresult = (event) => {
      const transcript = event.results[0][0].transcript
      if (transcript && onResult) onResult(transcript)
    }

    recognition.onerror = () => setListening(false)
    recognition.onend = () => setListening(false)

    recognitionRef.current = recognition
    recognition.start()
  }, [listening, onResult])

  const stop = useCallback(() => {
    if (recognitionRef.current) {
      recognitionRef.current.stop()
      recognitionRef.current = null
    }
    setListening(false)
  }, [])

  return { listening, supported, start, stop }
}
