import { useState, useEffect } from 'react'

const MOBILE_BREAKPOINT = 768

export type ViewportLayout = 'mobile' | 'desktop'

export function useViewport(): ViewportLayout {
  const [layout, setLayout] = useState<ViewportLayout>(() =>
    typeof window !== 'undefined' && window.innerWidth < MOBILE_BREAKPOINT
      ? 'mobile'
      : 'desktop'
  )

  useEffect(() => {
    const mq = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
    const handler = (e: MediaQueryListEvent) => setLayout(e.matches ? 'mobile' : 'desktop')
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])

  return layout
}
