import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

// Owner by default; individual tests override.
let mockProfile = { display_name: 'Owner', role: 'admin' }
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))

import ModelsPanel from '../core/settings/ModelsPanel.jsx'

// The shape every mock `fetch` in this file resolves to — deliberately NOT a
// full `Response` (no headers/status text/body stream): the component only
// ever reads `.ok`/`.status`/`.json()`, and `vi.stubGlobal` (unlike a direct
// `global.fetch =` assignment) accepts this narrower stand-in without lying
// about it being a real Response.
interface MockFetchResponse {
  ok: boolean
  status?: number
  json: () => Promise<unknown>
}

function mockModels(body: unknown, status = 200) {
  const fetchMock = vi.fn((url: string) => {
    if (String(url).includes('/api/models')) {
      return Promise.resolve({ ok: status === 200, status, json: () => Promise.resolve(body) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

beforeEach(() => { mockProfile = { display_name: 'Owner', role: 'admin' } })
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('ModelsPanel — RAG readiness + management', () => {
  it('hides management for non-owners', async () => {
    mockProfile = { display_name: 'User', role: 'user' }
    const fetchMock = mockModels({})
    render(<ModelsPanel />)
    expect(await screen.findByText(/available to the box owner only/i)).toBeTruthy()
    // Non-owner must NOT trigger the owner-only fetch.
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('shows the lexical fallback indicator when no model is installed', async () => {
    mockModels({
      embeddings: { dir: '/home/u/.vulos/models', models: [], rag_mode: 'lexical', needs_registry: true },
      chat_models: null, chat_models_error: 'no llmux gateway configured',
    })
    render(<ModelsPanel />)
    // The awaited element must be one that CANNOT be on screen before the box
    // has answered. This assertion pair used to await /Lexical retrieval/ —
    // which the panel rendered on its very first paint, from a default — so it
    // resolved against the loading state and the getByText below raced the
    // fetch. That is the whole of the flake: a `findBy*` that synchronizes on
    // nothing is a `getBy*` wearing an await.
    expect(await screen.findByText(/no llmux gateway configured/i)).toBeTruthy()
    // Exact, not /Lexical retrieval/i: the body copy below the badge also says
    // "the assistant is using sovereign lexical retrieval", so the regex matches
    // TWO elements once the listing has rendered and `findByText` throws
    // "Found multiple elements". The old assertion could therefore only ever
    // pass against the pre-load badge — it was ambiguous by construction the
    // moment the data it was supposedly waiting for arrived.
    expect(screen.getByText('Lexical retrieval')).toBeTruthy()
  })

  it('claims no retrieval mode before the box has answered', async () => {
    // A request that never settles: the panel has read nothing, and must say so
    // rather than name one of the three real modes.
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    render(<ModelsPanel />)
    expect(await screen.findByText(/Loading models/i)).toBeTruthy()
    expect(screen.getByText(/Retrieval quality not yet known/i)).toBeTruthy()
    for (const claim of ['Lexical retrieval', 'Degraded fallback', 'Semantic RAG active']) {
      expect(screen.queryByText(claim)).toBeNull()
    }
  })

  it('claims no retrieval mode when the models read fails', async () => {
    // A 403 tells us nothing about retrieval quality. Naming a mode here draws
    // a failed read as a designed state — the operator reads "no model is
    // installed" from what is actually "we could not look".
    mockModels({ error: 'owner only' }, 403)
    render(<ModelsPanel />)
    expect(await screen.findByRole('alert')).toBeTruthy()
    expect(screen.getByText(/Retrieval quality not yet known/i)).toBeTruthy()
    expect(screen.queryByText('Lexical retrieval')).toBeNull()
  })

  it('claims no retrieval mode when the box reports one it does not recognise', async () => {
    // An unrecognised rag_mode is narrowed away to undefined on the way in. It
    // must surface as "not known", not silently become the lexical default.
    mockModels({
      embeddings: { dir: '/models', models: [], rag_mode: 'quantum', needs_registry: true },
      chat_models: null, chat_models_error: 'no llmux gateway configured',
    })
    render(<ModelsPanel />)
    expect(await screen.findByText(/no llmux gateway configured/i)).toBeTruthy()
    expect(screen.getByText(/Retrieval quality not yet known/i)).toBeTruthy()
    expect(screen.queryByText('Lexical retrieval')).toBeNull()
  })

  it('shows the DEGRADED indicator honestly when tokenizer.json is missing', async () => {
    mockModels({
      embeddings: {
        dir: '/models',
        models: [{ name: 'model.onnx', active: true, has_tokenizer: false, size_bytes: 1024, sha256: 'abcdef0123456789' }],
        active_model: 'model.onnx', rag_mode: 'degraded', needs_registry: true,
      },
      chat_models: null, chat_models_error: 'x',
    })
    render(<ModelsPanel />)
    expect(await screen.findByText(/Degraded fallback/i)).toBeTruthy()
    // The honest guidance to install tokenizer.json must be present (appears in
    // several places: the badge copy, the import hint, and the model row badge).
    expect(screen.getAllByText(/tokenizer\.json/i).length).toBeGreaterThan(0)
    expect(screen.getByText('no tokenizer')).toBeTruthy()
  })

  it('shows the SEMANTIC indicator and chat models when fully installed', async () => {
    mockModels({
      embeddings: {
        dir: '/models',
        models: [{ name: 'all-MiniLM-L6-v2.onnx', active: true, has_tokenizer: true, size_bytes: 22 * 1024 * 1024, sha256: 'deadbeefcafef00d' }],
        active_model: 'all-MiniLM-L6-v2.onnx', rag_mode: 'semantic', needs_registry: true,
      },
      chat_models: { data: [{ id: 'gpt-4o-mini' }, { id: 'local-llama' }] },
    })
    render(<ModelsPanel />)
    expect(await screen.findByText(/Semantic RAG active/i)).toBeTruthy()
    expect(screen.getByText('gpt-4o-mini')).toBeTruthy()
    expect(screen.getByText('local-llama')).toBeTruthy()
  })

  it('surfaces a 403 as an owner-only message', async () => {
    mockModels({ error: 'owner only' }, 403)
    render(<ModelsPanel />)
    await waitFor(() => expect(screen.getByText(/box owner only/i)).toBeTruthy())
  })

  const catalogEntry = {
    id: 'all-MiniLM-L6-v2', name: 'all-MiniLM-L6-v2', dim: 384, recommended: true,
    description: '384-dim on-box embeddings',
    model: { size_bytes: 90405214 }, tokenizer: { size_bytes: 466247 },
  }

  it('offers a one-click Download of the curated catalog model', async () => {
    mockModels({
      embeddings: {
        dir: '/models', models: [], rag_mode: 'lexical', needs_registry: false,
        catalog: [catalogEntry],
        python_deps: { python3: true, onnxruntime: true, tokenizers: true, ready: true, install_hint: 'pip install onnxruntime tokenizers numpy' },
      },
      chat_models: null, chat_models_error: 'x',
    })
    render(<ModelsPanel />)
    // The recommended download action is present.
    expect(await screen.findByText(/Download a recommended model/i)).toBeTruthy()
    expect(screen.getByText('Recommended')).toBeTruthy()
    expect(screen.getByRole('button', { name: /^Download$/ })).toBeTruthy()
  })

  it('warns honestly when the python embed deps are missing', async () => {
    mockModels({
      embeddings: {
        dir: '/models', models: [], rag_mode: 'lexical', needs_registry: false,
        catalog: [catalogEntry],
        python_deps: { python3: true, onnxruntime: false, tokenizers: false, ready: false, install_hint: 'pip install onnxruntime tokenizers numpy' },
      },
      chat_models: null, chat_models_error: 'x',
    })
    render(<ModelsPanel />)
    expect(await screen.findByText(/on-box embedding runtime is missing/i)).toBeTruthy()
    // The exact pip install command must be shown so the operator can fix it.
    expect(screen.getByText(/pip install onnxruntime tokenizers numpy/i)).toBeTruthy()
  })

  it('downloads the catalog model and flips RAG to semantic', async () => {
    const fetchImpl = vi.fn((url: string, opts?: RequestInit): Promise<MockFetchResponse> => {
      if (String(url).includes('/api/models/download')) {
        // Assert the body carries ONLY an id (no arbitrary URL).
        if (typeof opts?.body !== 'string') throw new Error('expected a JSON string body')
        const body: unknown = JSON.parse(opts.body)
        expect(body).toEqual({ id: 'all-MiniLM-L6-v2' })
        return Promise.resolve({
          ok: true, status: 200,
          json: () => Promise.resolve({
            downloaded: { id: 'all-MiniLM-L6-v2' },
            embeddings: {
              dir: '/models',
              models: [{ name: 'model.onnx', active: true, has_tokenizer: true, size_bytes: 90405214, sha256: 'deadbeef' }],
              active_model: 'model.onnx', rag_mode: 'semantic', needs_registry: false, catalog: [catalogEntry],
              python_deps: { ready: true, install_hint: 'x' },
            },
          }),
        })
      }
      if (String(url).includes('/api/models')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({
          embeddings: { dir: '/models', models: [], rag_mode: 'lexical', needs_registry: false, catalog: [catalogEntry], python_deps: { ready: true, install_hint: 'x' } },
          chat_models: null, chat_models_error: 'x',
        }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    })
    vi.stubGlobal('fetch', fetchImpl)

    render(<ModelsPanel />)
    const btn = await screen.findByRole('button', { name: /^Download$/ })
    fireEvent.click(btn)
    // After download the badge flips to semantic.
    expect(await screen.findByText(/Semantic RAG active/i)).toBeTruthy()
    expect(fetchImpl).toHaveBeenCalledWith('/api/models/download', expect.objectContaining({ method: 'POST' }))
  })

  it('surfaces a failed catalog download honestly and stays lexical', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => {
      if (String(url).includes('/api/models/download')) {
        return Promise.resolve({ ok: false, status: 502, json: () => Promise.resolve({ error: 'checksum mismatch — nothing installed' }) })
      }
      if (String(url).includes('/api/models')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({
          embeddings: { dir: '/models', models: [], rag_mode: 'lexical', needs_registry: false, catalog: [catalogEntry], python_deps: { ready: true, install_hint: 'x' } },
          chat_models: null, chat_models_error: 'x',
        }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
    }))
    render(<ModelsPanel />)
    const btn = await screen.findByRole('button', { name: /^Download$/ })
    fireEvent.click(btn)
    // The error is announced (role=alert) and the badge stays honest (lexical).
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toMatch(/checksum mismatch/i)
    // The readiness badge stays honest (still lexical — nothing installed).
    expect(screen.getByText('Lexical retrieval')).toBeTruthy()
  })
})
