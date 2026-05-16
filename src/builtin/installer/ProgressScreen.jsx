import { useState, useEffect, useRef } from 'react'

const PHASE_LABELS = {
  partitioning: 'Partitioning disk',
  formatting: 'Formatting partitions',
  copying: 'Copying system files',
  bootloader: 'Installing bootloader',
  configuring: 'Configuring system',
  finalizing: 'Finalizing installation',
  done: 'Installation complete',
}

function ProgressRing({ percent, size = 96, stroke = 6 }) {
  const r = (size - stroke) / 2
  const circ = 2 * Math.PI * r
  const offset = circ - (percent / 100) * circ

  return (
    <svg width={size} height={size} className="rotate-[-90deg]">
      <circle
        cx={size / 2} cy={size / 2} r={r}
        stroke="#1e1e2e" strokeWidth={stroke} fill="none"
      />
      <circle
        cx={size / 2} cy={size / 2} r={r}
        stroke="#6366f1" strokeWidth={stroke} fill="none"
        strokeDasharray={circ}
        strokeDashoffset={offset}
        strokeLinecap="round"
        className="transition-[stroke-dashoffset] duration-500"
      />
    </svg>
  )
}

export default function ProgressScreen({ disk, onSuccess, onError }) {
  const [progress, setProgress] = useState(0)
  const [phase, setPhase] = useState('partitioning')
  const [log, setLog] = useState([])
  const [wsState, setWsState] = useState('connecting') // 'connecting' | 'open' | 'closed' | 'error'
  const [installStarted, setInstallStarted] = useState(false)
  const [installError, setInstallError] = useState(null)
  const wsRef = useRef(null)
  const logRef = useRef(null)

  // Kick off the install POST, then open WebSocket
  useEffect(() => {
    let cancelled = false

    const startInstall = async () => {
      try {
        const res = await fetch('/api/installer/install', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ disk: disk.path, confirm: true }),
        })
        if (!res.ok) {
          const body = await res.text().catch(() => `HTTP ${res.status}`)
          throw new Error(body || `HTTP ${res.status}`)
        }
        if (!cancelled) {
          setInstallStarted(true)
          connectWS()
        }
      } catch (e) {
        if (!cancelled) {
          setInstallError(e.message)
          onError(e.message)
        }
      }
    }

    const connectWS = () => {
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
      const ws = new WebSocket(`${proto}://${window.location.host}/api/installer/progress`)
      wsRef.current = ws

      ws.onopen = () => setWsState('open')

      ws.onmessage = (evt) => {
        if (cancelled) return
        try {
          const msg = JSON.parse(evt.data)

          if (msg.progress != null) setProgress(Math.min(100, Math.max(0, msg.progress)))
          if (msg.phase) setPhase(msg.phase)
          if (msg.log) {
            setLog(prev => {
              const next = [...prev, { text: msg.log, ts: Date.now() }]
              return next.slice(-200) // keep last 200 lines
            })
            // Auto-scroll
            requestAnimationFrame(() => {
              if (logRef.current) {
                logRef.current.scrollTop = logRef.current.scrollHeight
              }
            })
          }

          if (msg.phase === 'done' || msg.done === true) {
            ws.close()
            setProgress(100)
            setTimeout(() => {
              if (!cancelled) onSuccess()
            }, 800)
          }

          if (msg.error) {
            ws.close()
            if (!cancelled) onError(msg.error)
          }
        } catch {
          // non-JSON text line — treat as log
          if (!cancelled) {
            setLog(prev => [...prev, { text: evt.data, ts: Date.now() }].slice(-200))
          }
        }
      }

      ws.onerror = () => {
        if (!cancelled) setWsState('error')
      }

      ws.onclose = () => {
        if (!cancelled) setWsState('closed')
      }
    }

    startInstall()

    return () => {
      cancelled = true
      wsRef.current?.close()
    }
  }, [disk.path]) // eslint-disable-line react-hooks/exhaustive-deps

  const phaseLabel = PHASE_LABELS[phase] || phase || 'Installing…'

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* Top hero: ring + phase */}
      <div className="shrink-0 flex flex-col items-center pt-10 pb-6 px-6 gap-4">
        <div className="relative">
          <ProgressRing percent={progress} size={112} stroke={7} />
          <div className="absolute inset-0 flex items-center justify-center">
            <span className="text-xl font-bold text-neutral-100 tabular-nums">
              {Math.round(progress)}%
            </span>
          </div>
        </div>

        <div className="text-center">
          <p className="text-sm font-medium text-neutral-200">{phaseLabel}</p>
          <div className="flex items-center justify-center gap-1.5 mt-1">
            <ConnectionDot state={wsState} />
            <span className="text-[11px] text-neutral-600">
              {wsState === 'connecting' && 'Connecting…'}
              {wsState === 'open' && 'Live progress'}
              {wsState === 'closed' && 'Stream closed'}
              {wsState === 'error' && 'Connection error'}
            </span>
          </div>
        </div>

        {/* Linear bar as secondary indicator */}
        <div className="w-full max-w-xs h-1.5 bg-neutral-800 rounded-full overflow-hidden">
          <div
            className="h-full bg-indigo-500 rounded-full transition-[width] duration-500"
            style={{ width: `${progress}%` }}
          />
        </div>
      </div>

      {/* Log output */}
      <div
        ref={logRef}
        className="flex-1 overflow-y-auto min-h-0 mx-4 mb-4 rounded-lg
          bg-neutral-950 border border-neutral-800/40 p-3"
      >
        {!installStarted && !installError && (
          <div className="flex items-center gap-2 text-xs text-neutral-600 py-2">
            <span className="w-3 h-3 rounded-full border border-neutral-700 border-t-transparent animate-spin" />
            Starting installation…
          </div>
        )}
        {installError && (
          <div className="text-xs text-red-400 font-mono">
            Error starting install: {installError}
          </div>
        )}
        {log.map((line, i) => (
          <div key={i} className="flex gap-2 font-mono text-[11px] leading-relaxed">
            <span className="text-neutral-700 select-none shrink-0 tabular-nums">
              {new Date(line.ts).toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })}
            </span>
            <span className={`${line.text.startsWith('Error') || line.text.startsWith('error') ? 'text-red-400' : 'text-neutral-400'}`}>
              {line.text}
            </span>
          </div>
        ))}
        {log.length === 0 && installStarted && (
          <div className="text-xs text-neutral-700 py-2 font-mono">Waiting for output…</div>
        )}
      </div>

      <div className="shrink-0 px-6 pb-5 text-center">
        <p className="text-[11px] text-neutral-700">
          Do not power off. Installation may take several minutes.
        </p>
      </div>
    </div>
  )
}

function ConnectionDot({ state }) {
  const cls = {
    connecting: 'bg-amber-500 animate-pulse',
    open: 'bg-green-500',
    closed: 'bg-neutral-600',
    error: 'bg-red-500',
  }[state] || 'bg-neutral-600'

  return <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${cls}`} />
}
