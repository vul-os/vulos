import { describe, it, expect } from 'vitest'
import { classifyAsk, shouldAsk } from '../core/askRouting.js'

// The ask-routing heuristic decides when a ⌘K query is a QUESTION for the
// agentic assistant vs. a navigation/action query. Explicit prefixes must
// always win and be stripped; inferred signals must be conservative so they
// never hijack ordinary app navigation.

describe('classifyAsk — explicit prefixes', () => {
  it('routes a leading "?" to Ask and strips it', () => {
    const r = classifyAsk('?what did Dana send me')
    expect(r.ask).toBe(true)
    expect(r.explicit).toBe(true)
    expect(r.prompt).toBe('what did Dana send me')
  })

  it('routes "ask " to Ask and strips it', () => {
    const r = classifyAsk('ask summarize my inbox')
    expect(r.ask).toBe(true)
    expect(r.explicit).toBe(true)
    expect(r.prompt).toBe('summarize my inbox')
  })

  it('routes "ai " to Ask and strips it', () => {
    const r = classifyAsk('ai draft a reply')
    expect(r.ask).toBe(true)
    expect(r.explicit).toBe(true)
    expect(r.prompt).toBe('draft a reply')
  })
})

describe('classifyAsk — inferred', () => {
  it('infers Ask when the text ends with a question mark', () => {
    const r = classifyAsk('when is my next meeting?')
    expect(r.ask).toBe(true)
    expect(r.explicit).toBe(false)
    expect(r.prompt).toBe('when is my next meeting?')
  })

  it('infers Ask when it starts with a question word (multi-token)', () => {
    expect(shouldAsk('how do I export my data')).toBe(true)
    expect(shouldAsk('what needs my attention')).toBe(true)
  })
})

describe('classifyAsk — must NOT hijack navigation', () => {
  it('does not treat a bare app name as a question', () => {
    expect(shouldAsk('Terminal')).toBe(false)
    expect(shouldAsk('Calculator')).toBe(false)
    expect(shouldAsk('compose')).toBe(false)
  })

  it('does not treat a single question word as a question', () => {
    // A lone "is"/"how" is likely a fragment being typed, not a question.
    expect(shouldAsk('how')).toBe(false)
    expect(shouldAsk('is')).toBe(false)
  })

  it('empty input is not Ask', () => {
    const r = classifyAsk('')
    expect(r.ask).toBe(false)
    expect(r.prompt).toBe('')
  })
})
