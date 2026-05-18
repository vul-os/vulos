import { useState, useEffect } from 'react'

const MOBILE_BREAKPOINT = 768

export function useViewport() {
  const [layout, setLayout] = useState(() =>
    typeof window !== 'undefined' && window.innerWidth < MOBILE_BREAKPOINT
      ? 'mobile'
      : 'desktop'
  )

  useEffect(() => {
    const mq = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
    const handler = (e) => setLayout(e.matches ? 'mobile' : 'desktop')
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  return layout
}
