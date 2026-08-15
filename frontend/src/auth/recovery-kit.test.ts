// recovery-kit.test.ts — what the first-boot "Recovery Kit" actually contains.
//
// The step downloads a JSON file, calls it a recovery kit, and gates the wizard
// behind typing "confirm" to attest you have stored it safely. Before this
// pass, the file held a ULID, a hostname, an SSH fingerprint and a SHA-256 of
// itself. Four identifiers. No credential. Nothing in it could restore access
// to anything, and it could not have been otherwise: the 24-word master
// recovery phrase is minted by POST /api/auth/register, which the wizard did
// not call until the step AFTER this one.
//
// These tests assert on the CONTENT of the kit, because "the download works" is
// not the property that matters — a download of the wrong thing works fine.
import { describe, it, expect } from 'vitest'

import { IK06_buildKitObject, type SetupConfig } from './Setup'

const PHRASE =
  'ridge acid lumber velvet tunnel oyster prism candle harbor mimic fossil sprout ' +
  'anchor kettle marble opaque ribbon thicket vulture jasmine quartz drift ember nomad'

function cfg(over: Partial<SetupConfig> = {}): SetupConfig {
  return {
    deviceProfile: 'pc', locale: 'en', timezone: 'Africa/Johannesburg',
    wifiSSID: '', wifiPassword: '', displayName: 'Ada', username: 'ada', password: 'x', pin: '',
    IS05_ulid: '01J8ZC4K7QF2VN9YB3RXPD6MTA',
    IS05_hostname: 'vulos-box',
    IS05_storageEnabled: false, IS05_storageSkipped: true, IS05_storageSizeGb: 20,
    IS05_storagePassword: '', IS05_storagePassphrase: '', IS05_storageMode: 'local-fs',
    IS05_storageMinioEndpoint: '', IS05_storageMinioRegion: '', IS05_storageMinioBucket: '',
    IS05_storageMinioCredsRef: '',
    IS05_sshPubkey: '', IS05_sshFingerprint: 'SHA256:PvZndafVcd9HXWPivEJMuk7wGoaioueavFR7qmTC4x4',
    IS05_s3AccessKey: '', IS05_s3SecretKey: '',
    suiteEmail: true, suiteWorkspace: true,
    ...over,
  }
}

describe('the recovery kit carries the credential that recovers the account', () => {
  it('includes the master recovery phrase when setup captured one', () => {
    // THE point of the whole step. Before this change the phrase did not exist
    // yet when this ran, so no version of this assertion could have passed.
    const kit = IK06_buildKitObject(cfg(), PHRASE)
    expect(kit.master_recovery_phrase).toBe(PHRASE)
  })

  it('says so out loud when there is no phrase, instead of looking complete', () => {
    // A kit that silently omits the only credential is worse than one that is
    // missing: the user has a file, believes they are covered, and finds out
    // otherwise on the day they are locked out.
    const kit = IK06_buildKitObject(cfg(), '')
    expect(kit.master_recovery_phrase).toBeUndefined()
    expect(kit.master_recovery_phrase_note).toMatch(/no recovery phrase/i)
    expect(kit.master_recovery_phrase_note).toMatch(/Settings/)
  })

  it('never emits an empty-string phrase, which would read as "present"', () => {
    // `"master_recovery_phrase": ""` in the JSON would satisfy a key-presence
    // check while carrying nothing — the shape a hollow guard passes on.
    const kit = IK06_buildKitObject(cfg(), '')
    expect(Object.keys(kit)).not.toContain('master_recovery_phrase')
  })
})

describe('the rest of the kit', () => {
  it('records the identity the box reported', () => {
    const kit = IK06_buildKitObject(cfg(), PHRASE)
    expect(kit.ulid).toBe('01J8ZC4K7QF2VN9YB3RXPD6MTA')
    expect(kit.hostname).toBe('vulos-box')
    expect(kit.ssh_fingerprint).toMatch(/^SHA256:/)
    expect(Date.parse(kit.issued_at)).not.toBeNaN()
  })

  it('omits the storage block entirely when cluster storage is off', () => {
    expect(IK06_buildKitObject(cfg({ IS05_storageEnabled: false }), PHRASE).storage).toBeUndefined()
  })

  it('includes size and access key when cluster storage is on', () => {
    const kit = IK06_buildKitObject(
      cfg({ IS05_storageEnabled: true, IS05_storageSizeGb: 50, IS05_s3AccessKey: 'AKIA123' }),
      PHRASE,
    )
    expect(kit.storage).toEqual({ enabled: true, size_gb: 50, s3_access_key: 'AKIA123' })
  })
})
