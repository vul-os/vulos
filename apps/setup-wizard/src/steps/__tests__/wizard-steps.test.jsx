// wizard-steps.test.jsx — vitest unit tests for VulosAccountStep + IntentStep.
//
// Tests:
//   1. VulosAccountStep rejects external-domain "@gmail.com" input
//   2. VulosAccountStep applies the "@vulos.to" suffix correctly (suffix-in-chrome pattern)
//   3. IntentStep persists intent choice to the wizard config state via update()
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import VulosAccountStep from '../VulosAccountStep.jsx'
import IntentStep from '../IntentStep.jsx'

// ─── VulosAccountStep ────────────────────────────────────────────────────────

// Silence fetch calls from the availability check during these tests.
beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.resolve({
    ok: true,
    json: () => Promise.resolve({ available: true }),
  })))
})
afterEach(() => {
  vi.restoreAllMocks()
})

describe('VulosAccountStep — external-domain rejection', () => {
  it('shows an error and strips the @ when user types a gmail.com address', () => {
    const update = vi.fn()
    render(
      <VulosAccountStep
        config={{ username: '' }}
        update={update}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />
    )

    const input = screen.getByPlaceholderText('yourname')

    // Simulate typing "alice@gmail.com"
    fireEvent.change(input, { target: { value: 'alice@gmail.com' } })

    // The input value should be stripped to "alice" (the handle part)
    expect(input.value).toBe('alice')

    // An error message about external domains should appear
    expect(
      screen.getByText(/Only @vulos\.to addresses are supported/i)
    ).toBeInTheDocument()
  })
})

describe('VulosAccountStep — @vulos.to suffix chrome', () => {
  it('always renders the @vulos.to suffix as read-only chrome', () => {
    render(
      <VulosAccountStep
        config={{ username: '' }}
        update={vi.fn()}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />
    )

    // The suffix label should be present (rendered as a static span)
    expect(screen.getByText('@vulos.to')).toBeInTheDocument()
  })

  it('accepts handle-part-only input and does not prepend the domain to the value', () => {
    render(
      <VulosAccountStep
        config={{ username: '' }}
        update={vi.fn()}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />
    )

    const input = screen.getByPlaceholderText('yourname')
    fireEvent.change(input, { target: { value: 'myhandle' } })

    // The input value should only contain the handle, not "myhandle@vulos.to"
    expect(input.value).toBe('myhandle')
    // The suffix is still rendered separately
    expect(screen.getByText('@vulos.to')).toBeInTheDocument()
  })
})

// ─── IntentStep ───────────────────────────────────────────────────────────────

describe('IntentStep — intent persisted to wizard config', () => {
  it('calls update("intent", "business") when the Business card is clicked', () => {
    const update = vi.fn()
    render(
      <IntentStep
        config={{ intent: 'none' }}
        update={update}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />
    )

    const businessCard = screen.getByText('Business').closest('button')
    fireEvent.click(businessCard)

    expect(update).toHaveBeenCalledWith('intent', 'business')
  })

  it('calls update("intent", "personal") when the Personal card is clicked', () => {
    const update = vi.fn()
    render(
      <IntentStep
        config={{ intent: 'none' }}
        update={update}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />
    )

    const personalCard = screen.getByText('Personal').closest('button')
    fireEvent.click(personalCard)

    expect(update).toHaveBeenCalledWith('intent', 'personal')
  })

  it('calls update("intent", "none") and onNext when Skip is clicked', () => {
    const update = vi.fn()
    const onNext = vi.fn()
    render(
      <IntentStep
        config={{ intent: 'none' }}
        update={update}
        onNext={onNext}
        onPrev={vi.fn()}
      />
    )

    const skipButton = screen.getByText(/Skip — continue free/i)
    fireEvent.click(skipButton)

    expect(update).toHaveBeenCalledWith('intent', 'none')
    expect(onNext).toHaveBeenCalled()
  })
})
