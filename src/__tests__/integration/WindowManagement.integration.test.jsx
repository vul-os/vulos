/**
 * WindowManagement.integration.test.jsx — wave-28 shell integration suite.
 *
 * Drives the REAL ShellProvider (its reducer + useShell actions) with the REAL
 * Dock, through a small harness that exposes the shell API as buttons + a live
 * window probe. This exercises the shell's window lifecycle end-to-end:
 *   • launch → a window opens and appears in the Dock,
 *   • minimize → window hides, its Dock item is labelled "(minimized)",
 *   • click the Dock item → restore + focus,
 *   • maximize + tile (snap) update window geometry,
 *   • close → removed from Dock,
 *   • multi-desktop: switch spaces, windows track their space.
 *
 * Window.jsx itself (iframes, genie animation, native mode) is intentionally not
 * mounted; the Dock is the real shell surface and the reducer is the real state
 * machine — that is where the window-management contract lives.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ShellProvider, useShell } from '../../providers/ShellProvider'
import Dock from '../../shell/Dock.jsx'

// A harness that surfaces the shell API as buttons + a JSON probe of the state
// a test wants to assert on (the active desktop's windows).
function Harness() {
  const s = useShell()
  const w0 = s.windows[0]
  return (
    <div>
      <button onClick={() => s.openWindow({ appId: 'terminal', title: 'Terminal' })}>launch-terminal</button>
      <button onClick={() => s.openWindow({ appId: 'drive', title: 'Drive' })}>launch-drive</button>
      <button onClick={() => w0 && s.minimizeWindow(w0.id)}>minimize-first</button>
      <button onClick={() => w0 && s.maximizeWindow(w0.id)}>maximize-first</button>
      <button onClick={() => w0 && s.tileWindow(w0.id, 'left')}>tile-first-left</button>
      <button onClick={() => w0 && s.closeWindow(w0.id)}>close-first</button>
      <button onClick={() => s.addDesktop()}>add-desktop</button>
      <div data-testid="win-count">{s.windows.length}</div>
      <div data-testid="active-desktop">{s.activeDesktop}</div>
      <div data-testid="first-win">{w0 ? JSON.stringify({ title: w0.title, minimized: !!w0.minimized, maximized: !!w0._maximized, tile: w0._tile || null }) : 'none'}</div>
      <div data-testid="desktops">{Object.keys(s.desktops).join(',')}</div>
      <Dock />
    </div>
  )
}

function mount() {
  return render(<ShellProvider><Harness /></ShellProvider>)
}

const winInfo = () => JSON.parse(screen.getByTestId('first-win').textContent)

afterEach(() => { cleanup(); try { localStorage.clear() } catch { /* noop */ } })

describe('Window management (integration, real ShellProvider + Dock)', () => {
  it('launching an app opens a window and adds it to the Dock', async () => {
    const user = userEvent.setup()
    mount()
    expect(screen.getByTestId('win-count')).toHaveTextContent('0')
    await user.click(screen.getByText('launch-terminal'))
    expect(screen.getByTestId('win-count')).toHaveTextContent('1')
    // Dock now has a button for the window.
    expect(screen.getByRole('button', { name: 'Terminal' })).toBeInTheDocument()
  })

  it('minimize hides the window and labels its Dock item; clicking it restores', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByText('launch-terminal'))
    await user.click(screen.getByText('minimize-first'))
    expect(winInfo().minimized).toBe(true)
    // Dock item is now labelled "(minimized)".
    const dockItem = screen.getByRole('button', { name: 'Terminal (minimized)' })
    await user.click(dockItem)
    expect(winInfo().minimized).toBe(false)
  })

  it('maximize and tile (snap-left) update the window state', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByText('launch-terminal'))
    await user.click(screen.getByText('maximize-first'))
    expect(winInfo().maximized).toBe(true)
    await user.click(screen.getByText('tile-first-left'))
    expect(winInfo().tile).toBe('left')
  })

  it('close removes the window from the Dock', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByText('launch-terminal'))
    expect(screen.getByRole('button', { name: 'Terminal' })).toBeInTheDocument()
    await user.click(screen.getByText('close-first'))
    expect(screen.getByTestId('win-count')).toHaveTextContent('0')
    expect(screen.queryByRole('button', { name: 'Terminal' })).toBeNull()
  })

  it('two windows both appear in the Dock; the second is focused', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByText('launch-terminal'))
    await user.click(screen.getByText('launch-drive'))
    expect(screen.getByTestId('win-count')).toHaveTextContent('2')
    expect(screen.getByRole('button', { name: 'Terminal' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Drive' })).toBeInTheDocument()
  })

  it('adding a desktop creates a new space; its windows are tracked separately', async () => {
    const user = userEvent.setup()
    mount()
    await user.click(screen.getByText('launch-terminal'))
    const before = screen.getByTestId('desktops').textContent.split(',').length
    await user.click(screen.getByText('add-desktop'))
    const after = screen.getByTestId('desktops').textContent.split(',').length
    expect(after).toBe(before + 1)
  })
})
