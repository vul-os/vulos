// wizard-steps.test.jsx — vitest unit tests for VulosAccountStep + IntentStep.
//
// Tests:
//   1. VulosAccountStep rejects email-address ("@gmail.com") input — usernames only
//   2. VulosAccountStep keeps the input to the username part only
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

describe('VulosAccountStep — email-address rejection', () => {
  it('shows a hint and strips the @ when user types an email address', () => {
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

    // The input value should be stripped to "alice" (the username part)
    expect(input.value).toBe('alice')

    // A hint that this is a username, not an email address, should appear
    expect(
      screen.getByText(/Enter a username, not an email address/i)
    ).toBeInTheDocument()
  })
})

describe('VulosAccountStep — username-only input', () => {
  it('keeps the input to the username part only (no email suffix)', () => {
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

    // The input value should only contain the username
    expect(input.value).toBe('myhandle')
    // There is no email-domain suffix chrome in the username step
    expect(screen.queryByText(/@vulos\./)).not.toBeInTheDocument()
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
