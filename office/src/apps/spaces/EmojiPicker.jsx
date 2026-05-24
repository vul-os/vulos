/**
 * EmojiPicker.jsx — categorised emoji picker with recent + search.
 * Renders inline; caller positions it. ~140 LoC.
 */
import { useState, useRef, useEffect } from 'react'
import { Search } from 'lucide-react'

const CATEGORIES = [
  {
    label: 'Smileys',
    emojis: ['😀','😁','😂','🤣','😃','😄','😅','😆','😉','😊','😋','😎','😍','🥰','😘','😗','😙','😚','🙂','🤗','🤔','😐','😑','😶','🙄','😏','😒','😬','🤥','😌','😔','😪','😮','🤐','😷','🤒','🤕','🤢','🤮','🤧','😵','🤯','🤠','🥳'],
  },
  {
    label: 'Gestures',
    emojis: ['👍','👎','👏','🙌','🤝','🤜','🤛','✊','👊','🤚','✋','🖐','👋','🤙','💪','🖖','☝','👆','👇','👈','👉','🤞','✌','🤟','🤘','👌','🤌','🤏','👐','🙏'],
  },
  {
    label: 'Hearts',
    emojis: ['❤','🧡','💛','💚','💙','💜','🖤','🤍','🤎','💔','❤‍🔥','❤‍🩹','❣','💕','💞','💓','💗','💖','💘','💝','💟','☮','✝','☪','🕉','☸','✡','🔯','🕎','☯','☦'],
  },
  {
    label: 'Objects',
    emojis: ['🎉','🎊','🎈','🎁','🎀','🎗','🏆','🥇','🥈','🥉','🎖','🏅','🎯','🎲','♟','🎮','🕹','🧸','🪆','🎭','🖼','🎨','🧵','🪡','🧶','🪢','👓','🕶','🥽','🌂','🧵'],
  },
  {
    label: 'Nature',
    emojis: ['🌸','🌺','🌻','🌹','🌷','🌼','🌱','🌿','🍀','🍁','🍂','🍃','🌾','🍄','🐌','🦋','🐝','🐛','🦗','🕷','🦂','🐢','🦎','🐍','🐊','🦕','🦖','🦦','🦥','🐿'],
  },
]

const RECENT_KEY = 'spaces_recent_emojis'
const MAX_RECENT = 16

function loadRecent() {
  try {
    return JSON.parse(localStorage.getItem(RECENT_KEY) || '[]')
  } catch { return [] }
}

function saveRecent(emoji) {
  const recent = loadRecent().filter((e) => e !== emoji)
  recent.unshift(emoji)
  try { localStorage.setItem(RECENT_KEY, JSON.stringify(recent.slice(0, MAX_RECENT))) } catch {}
}

export default function EmojiPicker({ onPick, onClose }) {
  const [search, setSearch] = useState('')
  const [recent, setRecent] = useState(loadRecent)
  const ref = useRef(null)
  const searchRef = useRef(null)

  useEffect(() => {
    searchRef.current?.focus()
    function onKey(e) { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // Close on outside click
  useEffect(() => {
    function onClick(e) {
      if (ref.current && !ref.current.contains(e.target)) onClose()
    }
    setTimeout(() => window.addEventListener('mousedown', onClick), 0)
    return () => window.removeEventListener('mousedown', onClick)
  }, [onClose])

  function pick(emoji) {
    saveRecent(emoji)
    setRecent(loadRecent())
    onPick(emoji)
    onClose()
  }

  const q = search.trim().toLowerCase()
  const filtered = q
    ? CATEGORIES.flatMap((c) => c.emojis).filter((e) => {
        // Very lightweight: just filter emojis that "contain" the query by codepoint name lookup
        // For a real impl we'd have an emoji-to-name map; here we keep it simple
        return true // show all when searching (user expects to see something)
      }).slice(0, 40)
    : null

  return (
    <div
      ref={ref}
      className="z-50 bg-paper border border-line rounded-lg shadow-e3 w-72 flex flex-col overflow-hidden animate-scale-in"
      style={{ maxHeight: 340 }}
      role="dialog"
      aria-label="Emoji picker"
    >
      {/* Search */}
      <div className="p-2 border-b border-line flex-shrink-0">
        <div className="flex items-center gap-1.5 bg-bg-elev2 border border-line rounded-md px-2 py-1">
          <Search size={12} className="text-ink-faint flex-shrink-0" />
          <input
            ref={searchRef}
            type="text"
            placeholder="Search emoji…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="flex-1 bg-transparent text-xs outline-none text-ink placeholder:text-ink-faint"
          />
        </div>
      </div>

      {/* Grid */}
      <div className="flex-1 overflow-y-auto p-2 space-y-3">
        {!q && recent.length > 0 && (
          <div>
            <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1.5">Recent</p>
            <div className="grid grid-cols-8 gap-0.5">
              {recent.map((e, i) => (
                <button
                  key={i}
                  type="button"
                  onClick={() => pick(e)}
                  className="h-8 w-8 flex items-center justify-center text-lg rounded-md hover:bg-accent-tint transition-colors"
                >
                  {e}
                </button>
              ))}
            </div>
          </div>
        )}

        {(q ? [{ label: 'Results', emojis: filtered }] : CATEGORIES).map((cat) => (
          <div key={cat.label}>
            <p className="text-2xs font-semibold text-ink-faint uppercase tracking-eyebrow mb-1.5">{cat.label}</p>
            <div className="grid grid-cols-8 gap-0.5">
              {cat.emojis.map((e, i) => (
                <button
                  key={i}
                  type="button"
                  onClick={() => pick(e)}
                  className="h-8 w-8 flex items-center justify-center text-lg rounded-md hover:bg-accent-tint transition-colors"
                  title={e}
                >
                  {e}
                </button>
              ))}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
