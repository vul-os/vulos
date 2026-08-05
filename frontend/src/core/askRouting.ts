// askRouting.js — decides when a ⌘K query should be handed to the agentic
// assistant (the "Ask" section) instead of being treated as a navigation /
// action / mail query.
//
// The heuristic is deliberately explicit-first so the palette never feels like
// it's "guessing wrong": an unambiguous prefix (`?` or "ask ") always routes to
// Ask and strips the prefix. Absent a prefix we fall back to lightweight
// natural-language signals (question mark, leading question word) so that
// typing a real question still reaches the assistant. Pure + testable.

const QUESTION_WORDS = [
  'who', 'what', 'when', 'where', 'why', 'how', 'which', 'whose', 'whom',
  'can', 'could', 'should', 'would', 'will', 'is', 'are', 'am', 'do', 'does',
  'did', 'was', 'were', 'has', 'have', 'may', 'might',
]

// Explicit prefixes that FORCE the Ask route. Order matters: longest first so
// "ask " is stripped before a bare "?".
const ASK_PREFIXES = ['?', 'ask ', 'ai ']

/**
 * Classify a palette query for the Ask route.
 *
 * @param raw the raw query text
 * @returns { ask, explicit, prompt }
 *   • ask      — should this go to the assistant?
 *   • explicit — did the user force it with a prefix (vs. inferred)?
 *   • prompt   — the cleaned prompt to send (prefix stripped, trimmed)
 */
export interface AskClassification {
  ask: boolean
  explicit: boolean
  prompt: string
}

export function classifyAsk(raw: string | null | undefined): AskClassification {
  const text = (raw || '').trim()
  if (!text) return { ask: false, explicit: false, prompt: '' }

  const lower = text.toLowerCase()

  // 1) Explicit prefix — always Ask, strip the marker.
  for (const p of ASK_PREFIXES) {
    if (lower.startsWith(p)) {
      const prompt = text.slice(p.length).trim()
      return { ask: true, explicit: true, prompt }
    }
  }

  // 2) Inferred — natural-language question signals.
  //    a) ends with a question mark
  //    b) starts with a question word AND is more than one token (so a bare
  //       "is" or an app called "How" doesn't hijack navigation)
  const endsWithQ = text.endsWith('?')
  const tokens = lower.split(/\s+/)
  const startsWithQuestionWord = tokens.length > 1 && QUESTION_WORDS.includes(tokens[0])

  if (endsWithQ || startsWithQuestionWord) {
    return { ask: true, explicit: false, prompt: text }
  }

  return { ask: false, explicit: false, prompt: text }
}

/**
 * True when the query should be routed to the assistant.
 */
export function shouldAsk(raw: string | null | undefined): boolean {
  return classifyAsk(raw).ask
}
