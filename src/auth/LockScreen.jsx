import { useState, useEffect, useRef } from 'react'

function useTime() {
  const [now, setNow] = useState(new Date())
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return now
}

export default function LockScreen({ onUnlock, userName }) {
  const [pin, setPin] = useState('')
  const [error, setError] = useState(false)
  const [showInput, setShowInput] = useState(false)
  const inputRef = useRef(null)
  const now = useTime()

  const time = now.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  const date = now.toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' })

  useEffect(() => {
    const reveal = () => {
      setShowInput(true)
      setTimeout(() => inputRef.current?.focus(), 50)
    }
    const onKey = (e) => { if (!showInput && e.key !== 'Escape') reveal() }
    const onPointer = () => { if (!showInput) reveal() }
    window.addEventListener('keydown', onKey)
    window.addEventListener('pointerdown', onPointer)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('pointerdown', onPointer)
    }
  }, [showInput])

  const handleSubmit = async (e) => {
    e.preventDefault()
    try {
      const res = await fetch('/api/auth/pin/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin }),
      })
      const data = await res.json()
      if (data.valid) {
        onUnlock()
      } else if (!data.has_pin && pin.length === 0) {
        // No PIN set — just unlock
        onUnlock()
      } else {
        setError(true)
        setPin('')
        setTimeout(() => setError(false), 1500)
      }
    } catch {
      // Backend unreachable — allow unlock with empty PIN
      if (pin.length === 0) onUnlock()
    }
  }

  return (
    <div className="fixed inset-0 bg-neutral-950 flex flex-col items-center justify-center z-[200]">
      {/* Background */}
      <div className="absolute inset-0 pointer-events-none">
        <div className="absolute top-[25%] left-[35%] w-[500px] h-[500px] rounded-full bg-blue-600 opacity-[0.02] blur-[150px]" />
        <div className="absolute bottom-[25%] right-[25%] w-[400px] h-[400px] rounded-full bg-violet-600 opacity-[0.02] blur-[150px]" />
      </div>

      {/* Clock */}
      <div className="relative text-center mb-8">
        <div className="text-7xl font-extralight text-neutral-200 font-mono tracking-widest">
          {time}
        </div>
        <div className="text-lg text-neutral-500 mt-2 font-light">
          {date}
        </div>
      </div>

      {/* Unlock area */}
      {showInput ? (
        <form onSubmit={handleSubmit} className="relative flex flex-col items-center gap-4">
          {userName && <p className="text-sm text-neutral-500">{userName}</p>}
          <div className="flex items-center gap-2">
            <input
              ref={inputRef}
              type="password"
              value={pin}
              onChange={(e) => setPin(e.target.value.replace(/[^0-9]/g, ''))}
              placeholder="PIN"
              inputMode="numeric"
              maxLength={8}
              aria-label="Unlock PIN"
              aria-invalid={error}
              aria-describedby={error ? 'lockscreen-error' : undefined}
              style={error ? { borderColor: 'var(--status-danger)' } : undefined}
              className={`w-40 text-center text-lg tracking-[0.5em] bg-neutral-900/60 border rounded-xl px-4 py-3 text-white outline-none transition-colors
                ${error ? 'animate-[shake_0.3s_ease-in-out]' : 'border-neutral-800 focus:border-neutral-600'}`}
            />
          </div>
          <p id="lockscreen-error" role="alert" className="sr-only">
            {error ? 'Incorrect PIN, try again' : ''}
          </p>
          <button type="submit" className="text-sm text-neutral-600 hover:text-neutral-400 transition-colors">
            {pin.length > 0 ? 'Unlock' : 'Enter without PIN'}
          </button>
        </form>
      ) : (
        <p className="text-sm text-neutral-700 animate-pulse">
          Tap or press any key
        </p>
      )}
    </div>
  )
}
