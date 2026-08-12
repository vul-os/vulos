import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

// The onboarding "Enable private AI search?" step (MODEL-DL-01). Rendered inline
// by the setup wizard's ReadyStep after the owner account exists. Exercised here
// as a unit across its offer / downloading / done / error states.
import { PrivateAIStep } from '../auth/Setup.jsx'

const CATALOG_ENTRY = {
  id: 'all-MiniLM-L6-v2',
  name: 'all-MiniLM-L6-v2',
  dim: 384,
  recommended: true,
  model: { size_bytes: 90405214 },
  tokenizer: { size_bytes: 466247 },
}

// The shape every mock `fetch` in this file resolves to — deliberately NOT a
// full `Response` (no headers/status text/body stream): the component only
// ever reads `.ok`/`.status`/`.json()`, and `vi.stubGlobal` (unlike a direct
// `global.fetch =` assignment) accepts this narrower stand-in without lying
// about it being a real Response. Mirrors Vault.transfer.test.tsx.
interface MockFetchResponse {
  ok: boolean
  status?: number
  json: () => Promise<unknown>
}

// mockModels wires /api/models (the catalog + python-deps probe the step loads on
// mount) and /api/models/download (the install action), with per-call overrides.
function mockModels({
  deps = { ready: true, install_hint: 'x' },
  download,
}: {
  deps?: { ready: boolean; install_hint: string }
  download?: (url: string, opts?: RequestInit) => Promise<MockFetchResponse>
} = {}) {
  vi.stubGlobal('fetch', vi.fn((url: string, opts?: RequestInit) => {
    const u = String(url)
    if (u.includes('/api/models/download')) {
      if (!download) throw new Error('expected a download handler for this test')
      return download(url, opts)
    }
    if (u.includes('/api/models')) {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({
          embeddings: { catalog: [CATALOG_ENTRY], python_deps: deps },
        }),
      })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  }))
}

beforeEach(() => { vi.restoreAllMocks() })
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('PrivateAIStep — onboarding private-AI offer', () => {
  it('offers the recommended model with its true size, and can be skipped', async () => {
    const onDone = vi.fn()
    mockModels()
    render(<PrivateAIStep onDone={onDone} />)

    // Honest, optional framing + the real download size (~87 MB from the catalog).
    expect(await screen.findByText(/Enable private AI search\?/i)).toBeTruthy()
    expect(screen.getByText(/all-MiniLM-L6-v2/)).toBeTruthy()
    await waitFor(() => expect(screen.getByRole('button', { name: /Enable private AI search \(~87 MB\)/i })).toBeTruthy())

    // Skip proceeds without downloading anything.
    fireEvent.click(screen.getByRole('button', { name: /Skip for now/i }))
    expect(onDone).toHaveBeenCalledTimes(1)
  })

  it('warns honestly when the python embed deps are missing (never auto-installs)', async () => {
    mockModels({ deps: { ready: false, install_hint: 'pip install onnxruntime tokenizers numpy' } })
    render(<PrivateAIStep onDone={vi.fn()} />)
    expect(await screen.findByText(/needs the vulos-embed Python packages/i)).toBeTruthy()
    expect(screen.getByText(/pip install onnxruntime tokenizers numpy/)).toBeTruthy()
    expect(screen.getByText(/never installed automatically/i)).toBeTruthy()
  })

  it('downloads by catalog id only and shows the ready confirmation on success', async () => {
    const onDone = vi.fn()
    mockModels({
      download: (_url, opts) => {
        // The body must carry ONLY an id (no arbitrary URL — SSRF guard parity).
        if (typeof opts?.body !== 'string') throw new Error('expected a JSON string body')
        expect(JSON.parse(opts.body)).toEqual({ id: 'all-MiniLM-L6-v2' })
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
      },
    })
    render(<PrivateAIStep onDone={onDone} />)
    const btn = await screen.findByRole('button', { name: /Enable private AI search/i })
    fireEvent.click(btn)
    expect(await screen.findByText(/Semantic search is ready on your box/i)).toBeTruthy()
    // Continue proceeds after a successful install.
    fireEvent.click(screen.getByRole('button', { name: /Continue/i }))
    expect(onDone).toHaveBeenCalledTimes(1)
  })

  it('surfaces a download failure honestly and still lets the user continue', async () => {
    mockModels({
      download: () => Promise.resolve({
        ok: false, status: 500, json: () => Promise.resolve({ error: 'checksum mismatch' }),
      }),
    })
    render(<PrivateAIStep onDone={vi.fn()} />)
    const btn = await screen.findByRole('button', { name: /Enable private AI search/i })
    fireEvent.click(btn)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/checksum mismatch/i)
    // The error state exposes Continue so a failed download never traps setup.
    expect(screen.getByRole('button', { name: /Continue/i })).toBeTruthy()
  })
})
