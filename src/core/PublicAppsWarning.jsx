import { useState, useEffect } from 'react'

const visAPIPOLL_INTERVAL = 10_000 // 10 seconds

function useVisPublicApps() {
  const [visData, setVisData] = useState({ count: 0, hasPublic: false })

  useEffect(() => {
    let cancelled = false

    async function visFetch() {
      try {
        const res = await fetch('/api/apps/visibility')
        if (!res.ok) return
        const list = await res.json()
        if (cancelled) return
        const nonPrivate = list.filter(a => a.visibility !== 'private')
        const hasPublic = nonPrivate.some(a => a.visibility === 'public')
        setVisData({ count: nonPrivate.length, hasPublic })
      } catch {
        // network error — keep previous state
      }
    }

    visFetch()
    const id = setInterval(visFetch, visAPIPOLL_INTERVAL)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  return visData
}

export default function PublicAppsWarning() {
  const { count, hasPublic } = useVisPublicApps()

  if (count === 0) return null

  const visColor = hasPublic
    ? 'bg-red-500/90 text-white'
    : 'bg-yellow-400/90 text-neutral-900'

  const visLabel = hasPublic
    ? `${count} public app${count !== 1 ? 's' : ''}`
    : `${count} local app${count !== 1 ? 's' : ''}`

  function visHandleClick() {
    window.dispatchEvent(new CustomEvent('vulos:open-public-apps'))
  }

  return (
    <button
      onClick={visHandleClick}
      title={`${count} non-private app${count !== 1 ? 's' : ''} — click to manage`}
      className={`
        inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full
        text-[10px] font-semibold leading-none tracking-wide
        transition-opacity hover:opacity-80 cursor-pointer select-none
        ${visColor}
      `}
    >
      <svg
        viewBox="0 0 16 16"
        className="w-2.5 h-2.5 shrink-0"
        fill="currentColor"
        aria-hidden="true"
      >
        <path d="M8 1a7 7 0 100 14A7 7 0 008 1zM7 4h2v5H7V4zm0 6h2v2H7v-2z" />
      </svg>
      {visLabel}
    </button>
  )
}
