import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import Vault from './Vault'

// Vault.transfer.test.tsx — the import/export UI contract.
//
// These endpoints existed in the backend but were never mounted, so the UI never
// had a way to reach them: a user could not migrate off an incumbent password
// manager, and — worse — could not get their passwords back OUT. These tests pin
// the surface that fixes that, and in particular the rule that an import must
// report EXACTLY what happened. "Silent partial success" is a recurring bug class
// in this codebase: an import that quietly drops 40 of 52 rows while showing a
// green tick leaves the user believing they migrated when they did not.

const MASTER = 'correct-horse-battery-staple'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// asRecord narrows an untrusted request body before any field access — the
// mock server's own equivalent of the isRecord narrowing Vault.tsx uses at
// its real fetch boundaries. Throws (fails the test loudly) rather than
// silently reading fields off something that isn't the object shape a
// request body was expected to be.
function asRecord(x: unknown): Record<string, unknown> {
  if (!isRecord(x)) throw new Error('expected an object request body')
  return x
}

// The shape every mock `fetch` in this file resolves to — deliberately NOT
// a full `Response` (no headers/status text/body stream): the component only
// ever reads `.ok` and `.json()`, and `vi.stubGlobal` (unlike a direct
// `global.fetch =` assignment) accepts this narrower stand-in without lying
// about it being a real Response.
interface MockFetchResponse {
  ok: boolean
  status?: number
  json: () => Promise<unknown>
}

interface FetchCall {
  url: string
  opts: RequestInit
  body: unknown
}

// unlock the vault and land on the entry list.
async function openVault(user: UserEvent) {
  render(<Vault />)
  await user.type(screen.getByPlaceholderText('Master password'), MASTER)
  await user.click(screen.getByRole('button', { name: /unlock vault/i }))
  await screen.findByPlaceholderText('Search vault…')
}

async function openTransfer(user: UserEvent) {
  await user.click(screen.getByRole('button', { name: /import or export vault/i }))
  await screen.findByRole('tab', { name: 'Import' })
}

// A File whose FileReader path works under jsdom.
function csvFile(): File {
  return new File(
    ['name,url,username,password\nx,https://x.example,alice,pw1\n'],
    'chrome.csv',
    { type: 'text/csv' },
  )
}

// Pick the source format, THEN attach the file.
//
// Order matters, and that is the point: the file input's `accept` is driven by
// the selected format, so a .csv cannot be attached while "Bitwarden (.json)" is
// selected — the browser filters it out. Choosing the format first is exactly
// what a real user does.
async function chooseCsvAndUpload(user: UserEvent) {
  await user.selectOptions(screen.getByLabelText(/where are they coming from/i), 'chrome-csv')
  await user.upload(screen.getByLabelText(/^File/i), csvFile())
}

describe('Vault unlock', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(async (url: string): Promise<MockFetchResponse> => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({ status: 'unlocked' }) }
      // The REAL shape: GET /api/auth/vault/entries returns a BARE JSON ARRAY.
      if (url === '/api/auth/vault/entries') {
        return { ok: true, json: async () => ([{ id: 'e1', url: 'https://x.example', username: 'alice' }]) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }))
  })

  // Regression: the vault used to blank-screen on EVERY unlock.
  //
  // loadEntries did `setEntries(data.entries || data || [])`. For a bare array
  // `data.entries` is NOT undefined — it is Array.prototype.entries, a truthy
  // METHOD. React took it for a state-updater function, called it with no
  // receiver, threw "Cannot convert undefined or null to object", and unmounted
  // the tree. Build and unit tests were green throughout; the app was a white
  // rectangle. This test renders the real response shape and asserts the list
  // actually appears.
  it('renders the entry list after unlocking (no blank screen)', async () => {
    const user = userEvent.setup()
    render(<Vault />)
    await user.type(screen.getByPlaceholderText('Master password'), MASTER)
    await user.click(screen.getByRole('button', { name: /unlock vault/i }))

    expect(await screen.findByPlaceholderText('Search vault…')).toBeInTheDocument()
    expect(await screen.findByText('alice')).toBeInTheDocument()
  })
})

describe('Vault import/export', () => {
  let calls: FetchCall[]

  beforeEach(() => {
    calls = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, opts: RequestInit = {}): Promise<MockFetchResponse> => {
      calls.push({ url, opts, body: typeof opts.body === 'string' ? JSON.parse(opts.body) : null })

      if (url === '/api/auth/vault/unlock') {
        return { ok: true, json: async () => ({ status: 'unlocked' }) }
      }
      if (url === '/api/auth/vault/entries') {
        return { ok: true, json: async () => [] }
      }
      if (url === '/api/auth/vault/import') {
        return {
          ok: true,
          json: async () => ({
            parsed: 5,
            imported: 2,
            skipped: 2,
            errors: 1,
            warnings: ['1 entr(ies) could not be stored'],
          }),
        }
      }
      if (url === '/api/auth/vault/export') {
        return { ok: true, json: async () => ({ data: btoa('ciphertext') }) }
      }
      return { ok: false, status: 404, json: async () => ({ error: 'not found' }) }
    }))

    // jsdom has no object-URL plumbing for the download path.
    URL.createObjectURL = vi.fn(() => 'blob:mock')
    URL.revokeObjectURL = vi.fn()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('reports imported, skipped AND failed counts — never a bare success', async () => {
    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)

    await chooseCsvAndUpload(user)
    await user.click(screen.getByRole('button', { name: /^import$/i }))

    const status = await screen.findByRole('status')

    // All three outcomes are on screen — this is the anti-silent-partial rule.
    expect(status).toHaveTextContent('Imported')
    expect(status).toHaveTextContent('Skipped')
    expect(status).toHaveTextContent('Failed')
    expect(status).toHaveTextContent('2') // imported
    expect(status).toHaveTextContent('1') // failed
    expect(status).toHaveTextContent('5 entries found in the file.')

    // Server-side warnings are surfaced verbatim, not swallowed.
    expect(status).toHaveTextContent('1 entr(ies) could not be stored')
  })

  it('sends the chosen format and the file as base64', async () => {
    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)

    await chooseCsvAndUpload(user)
    await user.click(screen.getByRole('button', { name: /^import$/i }))

    await waitFor(() => {
      expect(calls.some(c => c.url === '/api/auth/vault/import')).toBe(true)
    })
    const req = calls.find(c => c.url === '/api/auth/vault/import')
    if (!req) throw new Error('expected an import request')
    const body = asRecord(req.body)
    expect(body.format).toBe('chrome-csv')
    expect(atob(String(body.data))).toContain('alice')
  })

  it('surfaces an import error instead of pretending it worked', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string): Promise<MockFetchResponse> => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({}) }
      if (url === '/api/auth/vault/entries') return { ok: true, json: async () => [] }
      if (url === '/api/auth/vault/import') {
        return { ok: false, status: 413, json: async () => ({ error: 'file contains 90000 entries, limit is 20000' }) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }))

    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)

    await chooseCsvAndUpload(user)
    await user.click(screen.getByRole('button', { name: /^import$/i }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('file contains 90000 entries, limit is 20000')
    // …and no success panel.
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('export requires the master password (step-up) and a matching backup password', async () => {
    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)
    await user.click(screen.getByRole('tab', { name: 'Export' }))

    // Mismatched backup passwords never reach the network.
    await user.type(screen.getByLabelText(/master password/i), MASTER)
    await user.type(screen.getByLabelText(/^password for the backup file/i), 'backup-password')
    await user.type(screen.getByLabelText(/repeat backup password/i), 'different-one')
    await user.click(screen.getByRole('button', { name: /export vault/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/do not match/i)
    expect(calls.some(c => c.url === '/api/auth/vault/export')).toBe(false)
  })

  it('export posts the master password as the step-up and downloads the blob', async () => {
    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)
    await user.click(screen.getByRole('tab', { name: 'Export' }))

    await user.type(screen.getByLabelText(/master password/i), MASTER)
    await user.type(screen.getByLabelText(/^password for the backup file/i), 'backup-password')
    await user.type(screen.getByLabelText(/repeat backup password/i), 'backup-password')
    await user.click(screen.getByRole('button', { name: /export vault/i }))

    await waitFor(() => {
      expect(calls.some(c => c.url === '/api/auth/vault/export')).toBe(true)
    })
    const req = calls.find(c => c.url === '/api/auth/vault/export')
    if (!req) throw new Error('expected an export request')
    const body = asRecord(req.body)
    // The master password is the step-up; the backup password only encrypts the file.
    expect(body.master_password).toBe(MASTER)
    expect(body.password).toBe('backup-password')

    expect(await screen.findByRole('status')).toHaveTextContent(/downloaded/i)
    expect(URL.createObjectURL).toHaveBeenCalled()
  })

  it('shows a wrong-master-password step-up failure as an error', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string): Promise<MockFetchResponse> => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({}) }
      if (url === '/api/auth/vault/entries') return { ok: true, json: async () => [] }
      if (url === '/api/auth/vault/export') {
        return { ok: false, status: 401, json: async () => ({ error: 'wrong master password' }) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    }))

    const user = userEvent.setup()
    await openVault(user)
    await openTransfer(user)
    await user.click(screen.getByRole('tab', { name: 'Export' }))

    await user.type(screen.getByLabelText(/master password/i), 'wrong')
    await user.type(screen.getByLabelText(/^password for the backup file/i), 'backup-password')
    await user.type(screen.getByLabelText(/repeat backup password/i), 'backup-password')
    await user.click(screen.getByRole('button', { name: /export vault/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/wrong master password/i)
  })
})
