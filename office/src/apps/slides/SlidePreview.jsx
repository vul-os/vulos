import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import DOMPurify from 'dompurify'

// Shared DOMPurify config: allow Tiptap/Reveal HTML tags, strip anything
// that could execute code (<script>, on* handlers, javascript: URLs, <iframe>).
const PURIFY_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'input', 'button'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'onblur',
                'onchange', 'onsubmit', 'onkeydown', 'onkeyup', 'onkeypress'],
}

function sanitize(html) {
  return DOMPurify.sanitize(html ?? '', PURIFY_CONFIG)
}

/**
 * SlidePreview — full-screen reveal.js presentation overlay.
 *
 * The presentation itself is reveal.js territory (its own themes), so the
 * design-system retint is intentionally light-touch: the close affordance is
 * the only piece of Vulos chrome on this surface.  Background stays
 * pitch-black so reveal themes (especially black/night) render correctly.
 */
export default function SlidePreview({ data, onClose }) {
  const containerRef = useRef(null)

  useEffect(() => {
    if (!containerRef.current) return
    let deck = null

    import('reveal.js').then(({ default: Reveal }) => {
      deck = new Reveal(containerRef.current, {
        embedded: true,
        transition: data.transition || 'slide',
        margin: 0.04,
        controls: true,
        progress: true,
        slideNumber: true,
        hash: false,
        keyboard: true,
        overview: true,
        center: true,
      })
      deck.initialize()
    })

    return () => { try { deck?.destroy() } catch { /* ignore */ } }
  }, [data])

  useEffect(() => {
    const handler = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-50 bg-black flex flex-col animate-fade-in">
      <div className="absolute top-4 right-4 z-10">
        <button
          onClick={onClose}
          aria-label="Exit presentation"
          title="Exit presentation (Esc)"
          className={[
            'inline-flex items-center justify-center h-9 w-9 rounded-md',
            'bg-black/55 text-white border border-white/10',
            'hover:bg-black/75 hover:border-white/20',
            'focus-visible:outline-none focus-visible:shadow-focus',
            'transition-[background,border-color] duration-fast ease-out',
          ].join(' ')}
        >
          <X size={18} />
        </button>
      </div>

      <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/reveal.js/5.1.0/reveal.min.css" />
      <link rel="stylesheet" href={`https://cdnjs.cloudflare.com/ajax/libs/reveal.js/5.1.0/theme/${data.theme || 'black'}.min.css`} />

      <div ref={containerRef} className="reveal flex-1 w-full">
        <div className="slides">
          {data.slides.map((slide) => (
            <section key={slide.id} data-background={slide.background || undefined}>
              {slide.title && <h2>{slide.title}</h2>}
              {/*
                IMPORTANT — slide.content is user-authored HTML.  It MUST go
                through the sanitize() wrapper above (DOMPurify with the
                shared PURIFY_CONFIG) before being passed to
                dangerouslySetInnerHTML.  Do not bypass this.
              */}
              <div dangerouslySetInnerHTML={{ __html: sanitize(slide.content) }} />
              {slide.notes && <aside className="notes">{slide.notes}</aside>}
            </section>
          ))}
        </div>
      </div>
    </div>
  )
}
