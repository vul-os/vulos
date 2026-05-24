/**
 * SearchBar.jsx — per-channel message search with operator support.
 * Operators: from:user, before:date, after:date, has:link, has:file.
 * Results highlighted inline.
 */
import { useState, useRef, useCallback } from 'react'
import { Search, X, ChevronDown, ChevronUp } from 'lucide-react'

/**
 * parseSearchQuery — parse a raw query string into a structured filter.
 * Returns { terms, from, before, after, hasLink, hasFile }
 */
export function parseSearchQuery(raw) {
  const tokens = raw.trim().split(/\s+/)
  const filter = {
    terms: [],
    from: null,
    before: null,
    after: null,
    hasLink: false,
    hasFile: false,
  }
  for (const tok of tokens) {
    if (tok.startsWith('from:')) {
      filter.from = tok.slice(5).toLowerCase()
    } else if (tok.startsWith('before:')) {
      filter.before = new Date(tok.slice(7))
    } else if (tok.startsWith('after:')) {
      filter.after = new Date(tok.slice(6))
    } else if (tok === 'has:link') {
      filter.hasLink = true
    } else if (tok === 'has:file') {
      filter.hasFile = true
    } else if (tok.length > 0) {
      filter.terms.push(tok.toLowerCase())
    }
  }
  return filter
}

/**
 * matchesFilter — test a single message against a parsed filter.
 */
export function matchesFilter(msg, filter) {
  const body = (msg.body || '').toLowerCase()
  const author = (msg.author_id || '').toLowerCase()
  const ts = new Date(msg.created_at)

  if (filter.from && !author.includes(filter.from)) return false
  if (filter.before && ts >= filter.before) return false
  if (filter.after && ts <= filter.after) return false
  if (filter.hasLink && !/https?:\/\//.test(body)) return false
  if (filter.hasFile && !msg.attachment) return false
  if (filter.terms.length > 0) {
    const haystack = `${body} ${author}`
    if (!filter.terms.every((t) => haystack.includes(t))) return false
  }
  return true
}

/**
 * highlightTerms — wrap matched terms in a <mark> span.
 * Returns the string with HTML <mark> tags inserted.
 */
export function highlightTerms(text, terms) {
  if (!terms || terms.length === 0) return text
  let result = text
  for (const term of terms) {
    const re = new RegExp(`(${term.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi')
    result = result.replace(re, '<mark class="search-highlight">$1</mark>')
  }
  return result
}

// ---- SearchBar component -----------------------------------------------------

/**
 * SearchBar — renders the search input + result list.
 *
 * Props:
 *   messages    — full message array from the store
 *   onJump      — (msg) => void  — scroll to message
 *   onClose     — () => void
 */
export default function SearchBar({ messages = [], onJump, onClose }) {
  const [query, setQuery] = useState('')
  const [resultIdx, setResultIdx] = useState(0)
  const inputRef = useRef(null)

  const filter = query.trim() ? parseSearchQuery(query) : null
  const results = filter
    ? messages.filter((m) => m.state !== 'deleted' && matchesFilter(m, filter))
    : []

  const jump = useCallback(
    (msg) => {
      onJump(msg)
    },
    [onJump]
  )

  function navResult(dir) {
    setResultIdx((i) => {
      const next = i + dir
      if (next < 0) return results.length - 1
      if (next >= results.length) return 0
      return next
    })
  }

  function formatTime(ts) {
    if (!ts) return ''
    return new Date(ts).toLocaleString([], { dateStyle: 'short', timeStyle: 'short' })
  }

  return (
    <div className="flex flex-col border-b border-line bg-bg-elev2 flex-shrink-0">
      {/* Input row */}
      <div className="flex items-center gap-2 px-3 py-2">
        <Search size={13} className="text-ink-faint flex-shrink-0" />
        <input
          ref={inputRef}
          autoFocus
          type="text"
          placeholder='Search messages… e.g. from:alice has:link "meeting notes"'
          value={query}
          onChange={(e) => { setQuery(e.target.value); setResultIdx(0) }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') onClose()
            if (e.key === 'Enter') {
              if (results.length > 0) {
                jump(results[resultIdx])
                navResult(1)
              }
            }
          }}
          className="flex-1 bg-transparent text-sm outline-none text-ink placeholder:text-ink-faint"
        />
        {query && (
          <span className="text-2xs text-ink-faint tabular-nums flex-shrink-0">
            {results.length} result{results.length !== 1 ? 's' : ''}
          </span>
        )}
        {results.length > 1 && (
          <>
            <button
              type="button"
              onClick={() => navResult(-1)}
              className="p-0.5 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
              title="Previous result"
            >
              <ChevronUp size={13} />
            </button>
            <button
              type="button"
              onClick={() => navResult(1)}
              className="p-0.5 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
              title="Next result"
            >
              <ChevronDown size={13} />
            </button>
          </>
        )}
        <button
          type="button"
          onClick={onClose}
          className="p-0.5 rounded-sm text-ink-faint hover:text-ink hover:bg-accent-tint transition-colors"
          title="Close search"
        >
          <X size={13} />
        </button>
      </div>

      {/* Results */}
      {results.length > 0 && (
        <div className="max-h-56 overflow-y-auto border-t border-line divide-y divide-line">
          {results.map((msg, i) => {
            const terms = filter?.terms || []
            const highlighted = highlightTerms(msg.body || '', terms)
            return (
              <button
                key={msg.id}
                type="button"
                onClick={() => jump(msg)}
                className={[
                  'w-full text-left px-4 py-2 transition-colors',
                  i === resultIdx ? 'bg-accent-tint' : 'hover:bg-bg-elev2',
                ].join(' ')}
              >
                <div className="flex items-baseline gap-2 mb-0.5">
                  <span className="text-xs font-semibold text-ink tracking-tightish">
                    {msg.author_id}
                  </span>
                  <span className="text-2xs text-ink-faint">{formatTime(msg.created_at)}</span>
                </div>
                <p
                  className="text-xs text-ink-muted line-clamp-2 leading-snug"
                  // eslint-disable-next-line react/no-danger
                  dangerouslySetInnerHTML={{ __html: highlighted }}
                />
              </button>
            )
          })}
        </div>
      )}

      {query && results.length === 0 && (
        <div className="px-4 py-2 text-xs text-ink-faint font-serif italic border-t border-line">
          No messages match.
        </div>
      )}

      {/* Operator hints */}
      {!query && (
        <div className="px-4 py-1.5 border-t border-line flex flex-wrap gap-x-3 gap-y-0.5">
          {['from:user','before:2025-01-01','after:2025-01-01','has:link','has:file'].map((hint) => (
            <code key={hint} className="text-2xs text-ink-faint">{hint}</code>
          ))}
        </div>
      )}
    </div>
  )
}
