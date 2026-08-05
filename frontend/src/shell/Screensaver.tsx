import { useState, useEffect } from 'react'

function useTime(): Date {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

export interface ScreensaverProps {
  /** Called on any dismiss gesture (click / pointer move / key press). Its
   *  callers (see App.jsx's dismissScreensaver) take no arguments, so this is
   *  declared arg-less rather than as one specific DOM event handler type —
   *  it is wired directly to onClick/onPointerMove/onKeyDown below, each of
   *  which TS allows a fewer-parameter callback to satisfy. */
  onDismiss: () => void
}

export default function Screensaver({ onDismiss }: ScreensaverProps) {
  const now = useTime()
  const [position, setPosition] = useState({ x: 50, y: 50 })

  // Slowly drift the clock around the screen (burn-in prevention)
  useEffect(() => {
    const id = setInterval(() => {
      setPosition(prev => ({
        x: ((prev.x + (Math.random() * 2 - 1)) + 100) % 100,
        y: ((prev.y + (Math.random() * 2 - 1)) + 100) % 100,
      }))
    }, 3000)
    return () => clearInterval(id)
  }, [])

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  return (
    <div
      className="fixed inset-0 z-[150] bg-black cursor-none"
      onClick={onDismiss}
      onPointerMove={onDismiss}
      onKeyDown={onDismiss}
      tabIndex={0}
    >
      <div
        className="absolute transition-all duration-[3000ms] ease-in-out"
        style={{ left: `${position.x}%`, top: `${position.y}%`, transform: 'translate(-50%, -50%)' }}
      >
        <div className="text-6xl font-extralight text-neutral-500 font-mono tracking-widest opacity-60">
          {time}
        </div>
        <div className="text-sm text-neutral-700 mt-1 text-center opacity-40">
          {date}
        </div>
      </div>
    </div>
  )
}
