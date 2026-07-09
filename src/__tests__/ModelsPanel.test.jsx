import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'

// Owner by default; individual tests override.
let mockProfile = { display_name: 'Owner', role: 'admin' }
vi.mock('../auth/AuthProvider', () => ({
  useAuth: () => ({ profile: mockProfile }),
}))

import ModelsPanel from '../core/settings/ModelsPanel.jsx'

function mockModels(body, status = 200) {
  global.fetch = vi.fn((url) => {
    if (String(url).includes('/api/models')) {
      return Promise.resolve({ ok: status === 200, status, json: () => Promise.resolve(body) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
  })
}

beforeEach(() => { mockProfile = { display_name: 'Owner', role: 'admin' } })
afterEach(() => cleanup())

describe('ModelsPanel — RAG readiness + management', () => {
  it('hides management for non-owners', async () => {
    mockProfile = { display_name: 'User', role: 'user' }
    mockModels({})
    render(<ModelsPanel />)
    expect(await screen.findByText(/available to the box owner only/i)).toBeTruthy()
    // Non-owner must NOT trigger the owner-only fetch.
    expect(global.fetch).not.toHaveBeenCalled()
  })

  it('shows the lexical fallback indicator when no model is installed', async () => {
    mockModels({
      embeddings: { dir: '/home/u/.vulos/models', models: [], rag_mode: 'lexical', needs_registry: true },
      chat_models: null, chat_models_error: 'no llmux gateway configured',
    })
    render(<ModelsPanel />)
    expect(await screen.findByText(/Lexical retrieval/i)).toBeTruthy()
    expect(screen.getByText(/no llmux gateway configured/i)).toBeTruthy()
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
    const fetchImpl = vi.fn((url, opts) => {
      if (String(url).includes('/api/models/download')) {
        // Assert the body carries ONLY an id (no arbitrary URL).
        const body = JSON.parse(opts.body)
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
    global.fetch = fetchImpl

    render(<ModelsPanel />)
    const btn = await screen.findByRole('button', { name: /^Download$/ })
    fireEvent.click(btn)
    // After download the badge flips to semantic.
    expect(await screen.findByText(/Semantic RAG active/i)).toBeTruthy()
    expect(fetchImpl).toHaveBeenCalledWith('/api/models/download', expect.objectContaining({ method: 'POST' }))
  })
})
