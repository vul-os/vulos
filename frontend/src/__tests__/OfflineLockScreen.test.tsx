import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import OfflineLockScreen from '../auth/OfflineLockScreen.jsx'

afterEach(cleanup)

const codedError = (code: string, extra: Record<string, unknown> = {}) =>
  Object.assign(new Error(code), { code, ...extra })

describe('OfflineLockScreen', () => {
  it('submits the typed password to onUnlock', async () => {
    const user = userEvent.setup()
    const onUnlock = vi.fn().mockResolvedValue(true)
    render(<OfflineLockScreen onUnlock={onUnlock} identity={{ name: 'Ada' }} />)

    expect(screen.getByText('Ada')).toBeInTheDocument()
    await user.type(screen.getByLabelText('Account password'), 'hunter2')
    await user.click(screen.getByRole('button', { name: 'Unlock' }))
    expect(onUnlock).toHaveBeenCalledWith('hunter2')
  })

  it('shows attempts-left on a wrong password and stays usable', async () => {
    const user = userEvent.setup()
    const onUnlock = vi.fn().mockRejectedValue(codedError('WRONG_PASSWORD', { attemptsLeft: 3 }))
    render(<OfflineLockScreen onUnlock={onUnlock} />)

    await user.type(screen.getByLabelText('Account password'), 'bad')
    await user.click(screen.getByRole('button', { name: 'Unlock' }))

    expect(await screen.findByText(/3 attempts left/i)).toBeInTheDocument()
    expect(screen.getByLabelText('Account password')).not.toBeDisabled()
  })

  it('permanently disables the input after a WIPED result (dead-end handled by the shell)', async () => {
    const user = userEvent.setup()
    const onUnlock = vi.fn().mockRejectedValue(codedError('WIPED'))
    render(<OfflineLockScreen onUnlock={onUnlock} />)

    await user.type(screen.getByLabelText('Account password'), 'bad')
    await user.click(screen.getByRole('button', { name: 'Unlock' }))

    expect(await screen.findByText(/too many attempts/i)).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('Account password')).toBeDisabled())
  })

  it('surfaces a corrupt-data result as a terminal message', async () => {
    const user = userEvent.setup()
    const onUnlock = vi.fn().mockRejectedValue(codedError('CORRUPT'))
    render(<OfflineLockScreen onUnlock={onUnlock} />)

    await user.type(screen.getByLabelText('Account password'), 'x')
    await user.click(screen.getByRole('button', { name: 'Unlock' }))

    expect(await screen.findByText(/corrupted/i)).toBeInTheDocument()
    expect(screen.getByLabelText('Account password')).toBeDisabled()
  })
})
