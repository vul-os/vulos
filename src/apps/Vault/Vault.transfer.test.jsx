import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Vault from './Vault'

// Vault.transfer.test.jsx — the import/export UI contract.
//
// These endpoints existed in the backend but were never mounted, so the UI never
// had a way to reach them: a user could not migrate off an incumbent password
// manager, and — worse — could not get their passwords back OUT. These tests pin
// the surface that fixes that, and in particular the rule that an import must
// report EXACTLY what happened. "Silent partial success" is a recurring bug class
// in this codebase: an import that quietly drops 40 of 52 rows while showing a
// green tick leaves the user believing they migrated when they did not.

const MASTER = 'correct-horse-battery-staple'

// unlock the vault and land on the entry list.
async function openVault(user) {
  render(<Vault />)
  await user.type(screen.getByPlaceholderText('Master password'), MASTER)
  await user.click(screen.getByRole('button', { name: /unlock vault/i }))
  await screen.findByPlaceholderText('Search vault…')
}

async function openTransfer(user) {
  await user.click(screen.getByRole('button', { name: /import or export vault/i }))
  await screen.findByRole('tab', { name: 'Import' })
}

// A File whose FileReader path works under jsdom.
function csvFile() {
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
async function chooseCsvAndUpload(user) {
  await user.selectOptions(screen.getByLabelText(/where are they coming from/i), 'chrome-csv')
  await user.upload(screen.getByLabelText(/^File/i), csvFile())
}

describe('Vault unlock', () => {
  beforeEach(() => {
    global.fetch = vi.fn(async (url) => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({ status: 'unlocked' }) }
      // The REAL shape: GET /api/auth/vault/entries returns a BARE JSON ARRAY.
      if (url === '/api/auth/vault/entries') {
        return { ok: true, json: async () => ([{ id: 'e1', url: 'https://x.example', username: 'alice' }]) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    })
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
  let calls

  beforeEach(() => {
    calls = []
    global.fetch = vi.fn(async (url, opts = {}) => {
      calls.push({ url, opts, body: opts.body ? JSON.parse(opts.body) : null })

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
    })

    // jsdom has no object-URL plumbing for the download path.
    global.URL.createObjectURL = vi.fn(() => 'blob:mock')
    global.URL.revokeObjectURL = vi.fn()
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
    expect(req.body.format).toBe('chrome-csv')
    expect(atob(req.body.data)).toContain('alice')
  })

  it('surfaces an import error instead of pretending it worked', async () => {
    global.fetch = vi.fn(async (url) => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({}) }
      if (url === '/api/auth/vault/entries') return { ok: true, json: async () => [] }
      if (url === '/api/auth/vault/import') {
        return { ok: false, status: 413, json: async () => ({ error: 'file contains 90000 entries, limit is 20000' }) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    })

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
    // The master password is the step-up; the backup password only encrypts the file.
    expect(req.body.master_password).toBe(MASTER)
    expect(req.body.password).toBe('backup-password')

    expect(await screen.findByRole('status')).toHaveTextContent(/downloaded/i)
    expect(global.URL.createObjectURL).toHaveBeenCalled()
  })

  it('shows a wrong-master-password step-up failure as an error', async () => {
    global.fetch = vi.fn(async (url) => {
      if (url === '/api/auth/vault/unlock') return { ok: true, json: async () => ({}) }
      if (url === '/api/auth/vault/entries') return { ok: true, json: async () => [] }
      if (url === '/api/auth/vault/export') {
        return { ok: false, status: 401, json: async () => ({ error: 'wrong master password' }) }
      }
      return { ok: false, status: 404, json: async () => ({}) }
    })

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
