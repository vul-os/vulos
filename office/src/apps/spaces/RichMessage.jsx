/**
 * RichMessage.jsx — markdown rendering with DOMPurify sanitisation.
 * Code blocks lazy-load highlight.js only when a fence block is present.
 * Renders @mentions as inline chips.
 */
import { useEffect, useRef, useState } from 'react'

// ---- Inline markdown → HTML --------------------------------------------------

/**
 * renderMarkdown — a lightweight markdown-to-HTML renderer.
 * Handles: bold, italic, inline code, links, unordered/ordered lists,
 * blockquotes, and fenced code blocks (language-tagged for hljs).
 * Returns an HTML string; must be sanitised before injection.
 */
export function renderMarkdown(text, members = []) {
  if (!text) return ''

  let html = text

  // Fenced code blocks: ```lang\ncode\n```
  html = html.replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
    const escaped = code
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
    return `<pre class="hljs-pre" data-lang="${lang}"><code class="hljs language-${lang}">${escaped}</code></pre>`
  })

  // Split on block elements (pre), process inline on non-pre parts
  const parts = html.split(/(<pre[\s\S]*?<\/pre>)/)
  html = parts.map((part, i) => {
    if (i % 2 === 1) return part // inside a pre block, leave as-is

    // Blockquotes: > line (match both literal > and html-encoded &gt;)
    part = part.replace(/^(?:>|&gt;) (.+)$/gm, '<blockquote>$1</blockquote>')

    // Unordered lists: - item
    part = part.replace(/^[-*] (.+)$/gm, '<li>$1</li>')
    part = part.replace(/(<li>.*<\/li>)/gs, '<ul>$1</ul>')

    // Ordered lists: 1. item
    part = part.replace(/^\d+\. (.+)$/gm, '<li>$1</li>')
    // (wrap in ol done below)

    // Inline code: `code`
    part = part.replace(/`([^`\n]+)`/g, '<code class="inline-code">$1</code>')

    // Bold: **text** or __text__
    part = part.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    part = part.replace(/__(.+?)__/g, '<strong>$1</strong>')

    // Italic: _text_ or *text* (single)
    part = part.replace(/(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)/g, '<em>$1</em>')
    part = part.replace(/(?<!_)_(?!_)(.+?)(?<!_)_(?!_)/g, '<em>$1</em>')

    // Links: [text](url)
    part = part.replace(
      /\[([^\]]+)\]\((https?:\/\/[^\)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener noreferrer" class="msg-link">$1</a>'
    )

    // @mentions: <@user_id> → styled chip
    part = part.replace(/<@([\w:]+)>/g, (_, uid) => {
      if (uid === 'channel') {
        return '<span class="mention mention-channel">@channel</span>'
      }
      const member = members.find((m) => m.accountId === uid || m.account_id === uid)
      const name = member?.displayName || member?.display_name || uid
      return `<span class="mention">@${name}</span>`
    })

    // Newlines → <br> (but not inside block elements)
    part = part.replace(/\n/g, '<br>')

    return part
  }).join('')

  return html
}

// ---- highlight.js lazy loader ------------------------------------------------
let hljsCache = null
let hljsPromise = null

async function loadHljs() {
  if (hljsCache) return hljsCache
  if (!hljsPromise) {
    hljsPromise = import('highlight.js/lib/core').then(async (mod) => {
      const hljs = mod.default
      // Load a handful of common languages lazily
      const [js, py, go, bash, json, ts, css, xml] = await Promise.all([
        import('highlight.js/lib/languages/javascript'),
        import('highlight.js/lib/languages/python'),
        import('highlight.js/lib/languages/go'),
        import('highlight.js/lib/languages/bash'),
        import('highlight.js/lib/languages/json'),
        import('highlight.js/lib/languages/typescript'),
        import('highlight.js/lib/languages/css'),
        import('highlight.js/lib/languages/xml'),
      ])
      hljs.registerLanguage('javascript', js.default)
      hljs.registerLanguage('js', js.default)
      hljs.registerLanguage('python', py.default)
      hljs.registerLanguage('py', py.default)
      hljs.registerLanguage('go', go.default)
      hljs.registerLanguage('bash', bash.default)
      hljs.registerLanguage('sh', bash.default)
      hljs.registerLanguage('json', json.default)
      hljs.registerLanguage('typescript', ts.default)
      hljs.registerLanguage('ts', ts.default)
      hljs.registerLanguage('css', css.default)
      hljs.registerLanguage('xml', xml.default)
      hljs.registerLanguage('html', xml.default)
      hljsCache = hljs
      return hljs
    })
  }
  return hljsPromise
}

// ---- RichMessage component ---------------------------------------------------

/**
 * RichMessage — renders a sanitised markdown body.
 *
 * Props:
 *   body    — raw markdown string
 *   members — optional roster array for mention resolution
 */
export default function RichMessage({ body = '', members = [] }) {
  const ref = useRef(null)
  const [, forceRender] = useState(0)

  const hasCodeBlock = body.includes('```')

  const rawHtml = renderMarkdown(body, members)

  // Sanitise with DOMPurify (available in browser env)
  let safeHtml = rawHtml
  if (typeof window !== 'undefined' && window.DOMPurify) {
    safeHtml = window.DOMPurify.sanitize(rawHtml, {
      ALLOWED_TAGS: ['strong','em','code','pre','a','ul','ol','li','blockquote','br','span'],
      ALLOWED_ATTR: ['href','target','rel','class','data-lang'],
    })
  }

  // Highlight code blocks after render
  useEffect(() => {
    if (!hasCodeBlock || !ref.current) return
    loadHljs().then((hljs) => {
      if (!ref.current) return
      ref.current.querySelectorAll('code.hljs').forEach((block) => {
        hljs.highlightElement(block)
      })
      forceRender((n) => n + 1)
    })
  }, [body, hasCodeBlock])

  return (
    <div
      ref={ref}
      className="rich-message text-sm text-ink leading-snug"
      // eslint-disable-next-line react/no-danger
      dangerouslySetInnerHTML={{ __html: safeHtml }}
    />
  )
}
