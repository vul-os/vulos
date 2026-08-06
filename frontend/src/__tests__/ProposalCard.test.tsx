/**
 * ProposalCard.test.jsx — locks the polished, SHARED confirmation-gate surface
 * (src/builtin/assistant/ProposalCard.jsx) used by the Assistant panel, Home and
 * the Command Palette. These assertions guard the legibility + a11y wins of the
 * polish pass so a future edit can't silently regress them:
 *
 *   • the "what will happen" key-facts (To / Subject / When …) render legibly,
 *   • untrusted-content ("from_content") surfaces as a role="alert",
 *   • Approve / Reject expose accessible names, and optional key hints,
 *   • the settled states are single, screen-reader-announced text nodes.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ProposalCard, StepTrace } from '../builtin/assistant/ProposalCard'

afterEach(() => cleanup())

const EMAIL = {
  id: 'p1',
  tool: 'send_email',
  summary: 'Send a reply to Bob confirming the meeting.',
  args: { to: 'bob@example.com', subject: 'Re: Sync', body: 'Sounds good — see you at 3.' },
}

describe('ProposalCard', () => {
  it('renders the verb, summary and the "what will happen" key facts', () => {
    render(<ProposalCard proposal={EMAIL} state="pending" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />)
    expect(screen.getByText(/Needs your approval · Send email/)).toBeInTheDocument()
    expect(screen.getByText(/Send a reply to Bob/)).toBeInTheDocument()
    // The target of the action is legible up front, not buried in the body.
    expect(screen.getByText('To')).toBeInTheDocument()
    expect(screen.getByText('bob@example.com')).toBeInTheDocument()
    expect(screen.getByText('Subject')).toBeInTheDocument()
    expect(screen.getByText('Re: Sync')).toBeInTheDocument()
    // The body still renders for review.
    expect(screen.getByText(/see you at 3/)).toBeInTheDocument()
  })

  it('is a labelled group so assistive tech announces it as one approval unit', () => {
    render(<ProposalCard proposal={EMAIL} state="pending" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />)
    expect(screen.getByRole('group', { name: /Send email.*Needs your approval/i })).toBeInTheDocument()
  })

  it('surfaces untrusted message-derived targets as an alert', () => {
    const tainted = { ...EMAIL, from_content: true, warning: 'Recipient came from the email body.' }
    render(<ProposalCard proposal={tainted} state="pending" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />)
    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent(/came from the email body/i)
  })

  it('calls back on Approve / Reject and exposes optional key hints', async () => {
    const onApprove = vi.fn()
    const onReject = vi.fn()
    const user = userEvent.setup()
    render(
      <ProposalCard proposal={EMAIL} state="pending" onApprove={onApprove} onReject={onReject}
        approveKey="Y" rejectKey="N" />,
    )
    const approve = screen.getByRole('button', { name: 'Approve' })
    const reject = screen.getByRole('button', { name: 'Reject' })
    expect(approve).toHaveAttribute('aria-keyshortcuts', 'Y')
    expect(reject).toHaveAttribute('aria-keyshortcuts', 'N')
    await user.click(approve)
    expect(onApprove).toHaveBeenCalledOnce()
    await user.click(reject)
    expect(onReject).toHaveBeenCalledOnce()
  })

  it('disables the buttons while busy and hides them once settled', () => {
    const { rerender } = render(
      <ProposalCard proposal={EMAIL} state="busy" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />,
    )
    expect(screen.getByRole('button', { name: 'Working…' })).toBeDisabled()

    rerender(<ProposalCard proposal={EMAIL} state="done" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />)
    expect(screen.queryByRole('button', { name: /Approve|Working/ })).toBeNull()
    expect(screen.getByText(/Approved and executed/)).toBeInTheDocument()

    rerender(<ProposalCard proposal={EMAIL} state="rejected" onApprove={() => {}} onReject={() => {}} approveKey={undefined} rejectKey={undefined} />)
    expect(screen.getByText(/Rejected — nothing was done/)).toBeInTheDocument()
  })
})

describe('StepTrace', () => {
  it('renders nothing without steps', () => {
    const { container } = render(<StepTrace steps={[]} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('is a collapsible, human-labelled tool trace', () => {
    render(<StepTrace steps={[
      { tool: 'search_mail', args: 'q=invoice' },
      { tool: 'read_thread', result: 'Invoice #4 due Friday' },
    ]} />)
    // Collapsed summary is scannable ("2 steps · show work").
    expect(screen.getByText('2 steps')).toBeInTheDocument()
    // The read-only tools are named in plain language.
    expect(screen.getByText('Searched mail')).toBeInTheDocument()
    expect(screen.getByText('Read a thread')).toBeInTheDocument()
  })
})
