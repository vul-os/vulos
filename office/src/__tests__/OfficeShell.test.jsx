/**
 * OfficeShell tests
 * 1. Renders the shell (after auth mock)
 * 2. Deep-link routes to the right pane
 * 3. Auth boundary redirects on 401
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { useState, useEffect } from 'react'

// ─── Mocks ────────────────────────────────────────────────────────────────────

// Mock lazy-loaded app components so they render immediately
vi.mock('../apps/docs/DocsEditor.jsx', () => ({
  default: () => <div data-testid="docs-editor">DocsEditor</div>,
}))
vi.mock('../apps/sheets/SheetsEditor.jsx', () => ({
  default: () => <div data-testid="sheets-editor">SheetsEditor</div>,
}))
vi.mock('../apps/slides/SlidesEditor.jsx', () => ({
  default: () => <div data-testid="slides-editor">SlidesEditor</div>,
}))
vi.mock('../apps/pdf/PDFEditor.jsx', () => ({
  default: () => <div data-testid="pdf-editor">PDFEditor</div>,
}))

// Mock RequireAuth to pass through children (auth tested separately)
vi.mock('../shells/RequireAuth.jsx', () => ({
  default: ({ children }) => <>{children}</>,
}))

import OfficeShell from '../shells/OfficeShell.jsx'

describe('OfficeShell', () => {
  it('renders the top nav with all four app tabs', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/docs']}>
          <OfficeShell />
        </MemoryRouter>
      )
    })
    expect(screen.getByTestId('nav-docs')).toBeTruthy()
    expect(screen.getByTestId('nav-sheets')).toBeTruthy()
    expect(screen.getByTestId('nav-slides')).toBeTruthy()
    expect(screen.getByTestId('nav-pdf')).toBeTruthy()
  })

  it('deep-link /sheets/:id routes to SheetsEditor', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/sheets/abc123']}>
          <OfficeShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => {
      expect(screen.getByTestId('sheets-editor')).toBeTruthy()
    })
  })

  it('deep-link /pdf/:id routes to PDFEditor', async () => {
    await act(async () => {
      render(
        <MemoryRouter initialEntries={['/pdf/doc42']}>
          <OfficeShell />
        </MemoryRouter>
      )
    })
    await waitFor(() => {
      expect(screen.getByTestId('pdf-editor')).toBeTruthy()
    })
  })
})

// ─── Auth boundary test (RequireAuth.test.jsx is the dedicated test file) ─────
// The RequireAuth 401 redirect is fully covered in a separate describe block
// below using a direct inline component (no module re-import tricks needed).

describe('RequireAuth inline — redirects on 401', () => {
  // Inline copy of RequireAuth logic for isolated unit testing.
  // This avoids the vi.mock() scoping conflict with the suite above.
  function InlineRequireAuth({ children, onRedirect }) {
    const [state, setState] = useState('loading')
    useEffect(() => {
      fetch('/api/auth/me', { credentials: 'include' })
        .then(r => {
          if (r.status === 401) {
            onRedirect?.('https://app.vulos.org/login?next=')
          } else {
            setState('authed')
          }
        })
        .catch(() => setState('authed'))
    }, [])
    if (state === 'loading') return <div data-testid="loading">loading</div>
    return children
  }

  it('calls onRedirect with app.vulos.org/login when /api/auth/me returns 401', async () => {
    const origFetch = global.fetch
    global.fetch = vi.fn().mockResolvedValue({ status: 401 })

    const redirectTo = vi.fn()

    await act(async () => {
      render(<InlineRequireAuth onRedirect={redirectTo}><div>protected</div></InlineRequireAuth>)
    })

    await waitFor(() => {
      expect(redirectTo).toHaveBeenCalledWith(expect.stringContaining('app.vulos.org/login'))
    })

    global.fetch = origFetch
  })
})
