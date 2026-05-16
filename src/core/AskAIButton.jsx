/**
 * AskAIButton — reusable affordance for AI-12.
 *
 * Dispatches `vulos:chat` (same CustomEvent used by Launchpad / Portal)
 * with the given `context` string as the detail payload.
 *
 * Usage:
 *   <AskAIButton context="What can I do in an empty ~/Documents folder?" />
 */

const SPARKLE = (
  <svg width="13" height="13" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
    <path d="M10 2a.75.75 0 01.75.75v1.5a.75.75 0 01-1.5 0v-1.5A.75.75 0 0110 2zm0 13a.75.75 0 01.75.75v1.5a.75.75 0 01-1.5 0v-1.5A.75.75 0 0110 15zm-8-5a.75.75 0 01.75-.75h1.5a.75.75 0 010 1.5h-1.5A.75.75 0 012 10zm13 0a.75.75 0 01.75-.75h1.5a.75.75 0 010 1.5h-1.5A.75.75 0 0115 10zM5.22 5.22a.75.75 0 011.06 0l1.06 1.06a.75.75 0 01-1.06 1.06L5.22 6.28a.75.75 0 010-1.06zm8.44 8.44a.75.75 0 011.06 0l1.06 1.06a.75.75 0 01-1.06 1.06l-1.06-1.06a.75.75 0 010-1.06zM14.78 5.22a.75.75 0 010 1.06l-1.06 1.06a.75.75 0 01-1.06-1.06l1.06-1.06a.75.75 0 011.06 0zM6.34 13.66a.75.75 0 010 1.06l-1.06 1.06a.75.75 0 01-1.06-1.06l1.06-1.06a.75.75 0 011.06 0z" />
  </svg>
)

export default function AskAIButton({ context, label = 'Ask AI', className = '' }) {
  const handleClick = () => {
    const message = context || 'How can I get started?'
    window.dispatchEvent(new CustomEvent('vulos:chat', { detail: message }))
  }

  return (
    <button
      onClick={handleClick}
      className={`inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium
        bg-violet-500/15 text-violet-300 border border-violet-500/25
        hover:bg-violet-500/25 hover:text-violet-200 hover:border-violet-500/40
        active:scale-95 transition-all duration-150 cursor-pointer ${className}`}
      title={context}
    >
      {SPARKLE}
      {label}
    </button>
  )
}
