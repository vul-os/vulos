// Setup.recoverykit.test.tsx — does the downloaded kit describe THIS box?
//
// recovery-kit.test.ts already asserts what IK06_buildKitObject puts in the
// kit. That is the builder in isolation. This file asserts the property the
// builder cannot have on its own: that the payload the step is holding — the
// bytes the owner downloads, and the SHA-256 shown on screen beside them —
// was built from the config as it stands NOW, not from an earlier version of
// it.
//
// The distinction matters because of how the kit is verified. The checksum is
// computed over the same object that is written to the file, so a kit built
// from stale values still verifies perfectly: the file matches itself. There
// is no downstream check anywhere that would catch it. The only place a stale
// kit becomes visible is here, against the config.
//
// The effect that builds the payload used to declare its dependencies by
// hand-listing four config fields while the builder read six (it also reads
// IS05_storageSizeGb and IS05_s3AccessKey). Not reachable as the wizard is
// wired today — every step is remounted by `key={current}` on navigation, and
// this is the one step with no `update` prop, so nothing can move underneath
// it — but a list that has to be re-derived every time the builder changes,
// with nothing to force that, is one field away from shipping a kit that does
// not match the box. This test is that forcing function.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'

// NavBar reaches for useI18n, which throws outside an I18nProvider.
vi.mock('../core/i18n', () => ({
  useI18n: () => ({ t: (k: string) => k, setLocale: () => {}, locale: 'en' }),
}))

import { IS05_RecoveryKitStep, type SetupConfig } from './Setup'

const PHRASE =
  'ridge acid lumber velvet tunnel oyster prism candle harbor mimic fossil sprout ' +
  'anchor kettle marble opaque ribbon thicket vulture jasmine quartz drift ember nomad'

function cfg(over: Partial<SetupConfig> = {}): SetupConfig {
  return {
    deviceProfile: 'pc', locale: 'en', timezone: 'Africa/Johannesburg',
    wifiSSID: '', wifiPassword: '', displayName: 'Ada', username: 'ada', password: 'x', pin: '',
    IS05_ulid: '01J8ZC4K7QF2VN9YB3RXPD6MTA',
    IS05_hostname: 'vulos-box',
    IS05_storageEnabled: true, IS05_storageSkipped: false, IS05_storageSizeGb: 20,
    IS05_storagePassword: '', IS05_storagePassphrase: '', IS05_storageMode: 'local-fs',
    IS05_storageMinioEndpoint: '', IS05_storageMinioRegion: '', IS05_storageMinioBucket: '',
    IS05_storageMinioCredsRef: '',
    IS05_sshPubkey: '', IS05_sshFingerprint: 'SHA256:PvZndafVcd9HXWPivEJMuk7wGoaioueavFR7qmTC4x4',
    IS05_s3AccessKey: '', IS05_s3SecretKey: '',
    suiteEmail: true, suiteWorkspace: true,
    ...over,
  } as SetupConfig
}

function renderKit(config: SetupConfig) {
  return render(
    <IS05_RecoveryKitStep
      config={config}
      masterPhrase={PHRASE}
      onNext={vi.fn()}
      onPrev={vi.fn()}
    />,
  )
}

/** The SHA-256 the step displays, once it has one. 64 hex chars, its own row. */
async function shownChecksum(): Promise<string> {
  const el = await screen.findByText(/^[0-9a-f]{64}$/)
  return el.textContent ?? ''
}

describe('the recovery kit the step is holding describes the current config', () => {
  beforeEach(() => {
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  })
  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('shows a checksum for the kit it actually built', async () => {
    renderKit(cfg())
    const sum = await shownChecksum()
    expect(sum).toHaveLength(64)
  })

  // The regression guard. IS05_storageSizeGb is one of the two fields the old
  // hand-written dependency list omitted, and it is read by the builder, so a
  // change to it MUST produce a different kit — a different size_gb, therefore
  // different bytes, therefore a different SHA-256.
  it('rebuilds the kit when a storage size the builder reads changes', async () => {
    const { rerender } = renderKit(cfg({ IS05_storageSizeGb: 20 }))
    const before = await shownChecksum()

    rerender(
      <IS05_RecoveryKitStep
        config={cfg({ IS05_storageSizeGb: 50 })}
        masterPhrase={PHRASE}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />,
    )

    // The visible contradiction this prevents: the "In this kit" panel reads
    // config directly, so it updates immediately and says 50 GB...
    await waitFor(() => {
      expect(screen.getByText(/Enabled · 50 GB/)).toBeInTheDocument()
    })
    // ...while the checksum beside it, and the file the button downloads,
    // would still be the 20 GB kit unless the payload was rebuilt.
    await waitFor(async () => {
      expect(await shownChecksum()).not.toBe(before)
    })
  })

  it('rebuilds the kit when the hostname changes', async () => {
    const { rerender } = renderKit(cfg({ IS05_hostname: 'study' }))
    const before = await shownChecksum()

    rerender(
      <IS05_RecoveryKitStep
        config={cfg({ IS05_hostname: 'studio' })}
        masterPhrase={PHRASE}
        onNext={vi.fn()}
        onPrev={vi.fn()}
      />,
    )

    await waitFor(async () => {
      expect(await shownChecksum()).not.toBe(before)
    })
  })
})
