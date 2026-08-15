// ssh-keyformat.test.ts — the setup wizard's SSH key encoders, checked against
// output from the real `ssh-keygen`.
//
// WHY THIS EXISTS, precisely.
//
// The wizard's SSH step generates a keypair in the browser, shows you the
// private key, makes you tick "I have saved this private key in a secure
// location", and posts the public key to the box. Two of the three artefacts it
// produced were wrong, and nothing in the product could have noticed, because
// nothing ever fed them to ssh:
//
//   1. The private key was exported as PKCS#8 (`-----BEGIN PRIVATE KEY-----`).
//      OpenSSH reads PEM/PKCS#8 via OpenSSL for RSA and ECDSA, but an Ed25519
//      private key is only accepted in OpenSSH's own format. So the file the
//      user was told to save was not one `ssh -i` would load. The purpose of
//      the step is remote access to the box, and it handed out a key that does
//      not open it.
//
//   2. The fingerprint was SHA-256 over the 32 RAW public-key bytes. OpenSSH
//      fingerprints the public-key WIRE BLOB. The displayed `SHA256:…` was
//      therefore a real digest of the wrong bytes — it matched nothing the user
//      could compare it to, which is the only reason to show a fingerprint.
//
// A test written from the same understanding as the code would have agreed with
// the code and stayed green. So the vectors below are not hand-derived: they
// are a real keypair from
//
//     ssh-keygen -t ed25519 -N '' -C 'ada@vulos-box' -f id_ed25519
//
// decomposed into its seed and public key, with ssh-keygen's own checkint. The
// encoder must reproduce that file BYTE FOR BYTE, and the fingerprint must
// equal what `ssh-keygen -lf` printed for it. That is an external oracle, not
// this repository agreeing with itself — the failure mode recorded across this
// suite (a vector corpus agreeing with the implementation that generated it).
import { describe, it, expect } from 'vitest'

import {
  sshString,
  sshEd25519PubBlob,
  sshEd25519AuthorizedKey,
  sshEd25519PrivateKeyFile,
  ed25519SeedFromPkcs8,
} from './Setup'

const hex = (s: string) => Uint8Array.from(s.match(/../g)!.map((b) => parseInt(b, 16)))

// ── Reference keypair, produced by OpenSSH ────────────────────────────────
const SEED = hex('4102d9539a56cf385bb71dfadd70681b880dbc457191d9b91264a3e0cb7790b0')
const PUB = hex('8c51a57d4aafc7efabc6cad0eb04cf9a6cee2cb9c67f5fd7e97b1da384bbe821')
const COMMENT = 'ada@vulos-box'
const CHECKINT = 0xac25eb55

/** Exactly what ssh-keygen wrote to id_ed25519. */
const REFERENCE_PRIVATE_KEY = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACCMUaV9Sq/H76vGytDrBM+abO4sucZ/X9fpex2jhLvoIQAAAJCsJetVrCXr
VQAAAAtzc2gtZWQyNTUxOQAAACCMUaV9Sq/H76vGytDrBM+abO4sucZ/X9fpex2jhLvoIQ
AAAEBBAtlTmlbPOFu3HfrdcGgbiA28RXGR2bkSZKPgy3eQsIxRpX1Kr8fvq8bK0OsEz5ps
7iy5xn9f1+l7HaOEu+ghAAAADWFkYUB2dWxvcy1ib3g=
-----END OPENSSH PRIVATE KEY-----
`

/** Exactly what ssh-keygen wrote to id_ed25519.pub. */
const REFERENCE_PUBLIC_KEY =
  'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIxRpX1Kr8fvq8bK0OsEz5ps7iy5xn9f1+l7HaOEu+gh ada@vulos-box'

/** Exactly what `ssh-keygen -lf id_ed25519.pub` printed. */
const REFERENCE_FINGERPRINT = 'SHA256:PvZndafVcd9HXWPivEJMuk7wGoaioueavFR7qmTC4x4'

/** The scheme the browser hands us the private key in (RFC 8410 §7). */
const REFERENCE_PKCS8 = hex(
  '302e020100300506032b657004220420' +
    '4102d9539a56cf385bb71dfadd70681b880dbc457191d9b91264a3e0cb7790b0',
)

describe('sshString', () => {
  it('prefixes a 4-byte big-endian length', () => {
    expect(Array.from(sshString('abc'))).toEqual([0, 0, 0, 3, 97, 98, 99])
  })

  it('encodes the empty string as a bare zero length', () => {
    expect(Array.from(sshString(''))).toEqual([0, 0, 0, 0])
  })
})

describe('sshEd25519AuthorizedKey', () => {
  it('reproduces the authorized_keys line ssh-keygen wrote', () => {
    expect(sshEd25519AuthorizedKey(PUB, COMMENT)).toBe(REFERENCE_PUBLIC_KEY)
  })
})

describe('the public-key blob, which is what a fingerprint is taken over', () => {
  it('digests to the fingerprint ssh-keygen -lf printed', async () => {
    const digest = await crypto.subtle.digest('SHA-256', sshEd25519PubBlob(PUB) as BufferSource)
    const b64 = Buffer.from(new Uint8Array(digest)).toString('base64').replace(/=+$/, '')
    expect(`SHA256:${b64}`).toBe(REFERENCE_FINGERPRINT)
  })

  it('differs from the digest of the raw 32 public-key bytes — the shipped defect', async () => {
    // The exact string the wizard used to display for THIS key. It is a
    // perfectly valid SHA-256 of something, which is precisely why nobody
    // caught it: it looks like a fingerprint and matches nothing on the box.
    // Both values are pinned, so swapping the input back is a red test rather
    // than a product that silently shows an uncomparable string.
    const digest = await crypto.subtle.digest('SHA-256', PUB as BufferSource)
    const b64 = Buffer.from(new Uint8Array(digest)).toString('base64').replace(/=+$/, '')
    expect(`SHA256:${b64}`).toBe('SHA256:orqR8D6jXQA8ZLaPBp0d6YN3X97+Z0rwLw/lyNYZRRw')
    expect(`SHA256:${b64}`).not.toBe(REFERENCE_FINGERPRINT)
  })
})

describe('sshEd25519PrivateKeyFile', () => {
  it('reproduces ssh-keygen\'s own file byte for byte', () => {
    expect(sshEd25519PrivateKeyFile(SEED, PUB, COMMENT, CHECKINT)).toBe(REFERENCE_PRIVATE_KEY)
  })

  it('announces itself as an OPENSSH key, not PKCS#8', () => {
    // The distinction the defect turned on: ssh will not load an Ed25519
    // private key from a `-----BEGIN PRIVATE KEY-----` (PKCS#8) file.
    const out = sshEd25519PrivateKeyFile(SEED, PUB, COMMENT, CHECKINT)
    expect(out.startsWith('-----BEGIN OPENSSH PRIVATE KEY-----\n')).toBe(true)
    expect(out).not.toContain('-----BEGIN PRIVATE KEY-----')
  })

  it('pads the private section to the 8-byte block size', () => {
    // The reference comment happens to need no padding, so the padding rule is
    // unexercised by the byte-for-byte test above. A one-character comment
    // shifts the length and must produce 1..7 trailing bytes of 1,2,3,…
    const out = sshEd25519PrivateKeyFile(SEED, PUB, 'a', CHECKINT)
    const b64 = out.split('\n').filter((l) => !l.startsWith('-----')).join('')
    const blob = Buffer.from(b64, 'base64')
    // The private section is the last SSH string in the blob.
    const len = blob.readUInt32BE(blob.length - 4 - readTrailingLen(blob))
    expect(len % 8).toBe(0)
  })

  it('refuses a seed or public key that is not 32 bytes', () => {
    // A silent truncation here yields a well-formed file that does not match
    // the public key the box was authorised with — the worst possible outcome,
    // because everything looks like it worked until the first ssh attempt.
    expect(() => sshEd25519PrivateKeyFile(SEED.slice(0, 31), PUB, COMMENT)).toThrow(/32 bytes/)
    expect(() => sshEd25519PrivateKeyFile(SEED, PUB.slice(0, 16), COMMENT)).toThrow(/32 bytes/)
  })
})

/** Length of the trailing private section, read back out of the blob. */
function readTrailingLen(blob: Buffer): number {
  // Walk the header the same way a reader would, to find where the private
  // section's length prefix sits.
  let off = 'openssh-key-v1\0'.length
  const skip = () => { const n = blob.readUInt32BE(off); off += 4 + n }
  skip() // ciphername
  skip() // kdfname
  skip() // kdfoptions
  off += 4 // nkeys
  skip() // public key blob
  return blob.length - off - 4
}

describe('ed25519SeedFromPkcs8', () => {
  it('extracts the seed WebCrypto wraps in PKCS#8', () => {
    expect(Array.from(ed25519SeedFromPkcs8(REFERENCE_PKCS8))).toEqual(Array.from(SEED))
  })

  it('rejects a buffer that is not a 48-byte Ed25519 PrivateKeyInfo', () => {
    expect(() => ed25519SeedFromPkcs8(new Uint8Array(32))).toThrow(/length/)
  })

  it('rejects a 48-byte buffer whose OCTET STRING header is not where RFC 8410 puts it', () => {
    // Guards against "take the last 32 bytes" degrading into a blind slice: a
    // buffer of the right SIZE but the wrong SHAPE must not silently yield 32
    // arbitrary bytes that then get published as a key.
    const wrong = new Uint8Array(REFERENCE_PKCS8)
    wrong[14] = 0x05
    expect(() => ed25519SeedFromPkcs8(wrong)).toThrow(/RFC 8410/)
  })
})
