import { useState, useEffect, useCallback, useRef } from 'react'
import QRCode from 'qrcode'
import { Section, Card, InfoList, InfoRow, Banner, EmptyState } from './ui'

// ---------------------------------------------------------------------------
// LANRootCertPanel — Settings → Network → Browser Trust (ROOTDIST-01).
//
// # Why this is its own panel and not part of Native Pairing
//
// The two sit next to each other in the nav and answer the same owner question
// ("how do I connect to my box safely?"), but they are OPPOSITE trust models
// and merging them would blur the one distinction D101 spends its length
// drawing:
//
//   - Native Pairing pins the box's own key by SPKI. No certificate authority
//     is in the path at all — that was D96-D's whole point.
//   - Browser Trust installs a certificate AUTHORITY on the device. It exists
//     only because a browser has no pinning flow a non-technical owner can
//     complete, and no public CA may ever issue for `.local`.
//
// One panel showing a "fingerprint" that means the box's public key in one half
// and a CA certificate's digest in the other would invite an owner to compare
// the wrong two values. They are adjacent, and separate.
//
// # What this panel must never do
//
// Claim the first download is authenticated. It is not: the owner fetches the
// root over TLS the device does not yet trust. That is acceptable — the root is
// a public certificate and its fingerprint is verifiable out of band — but it
// has to be SAID, and the fingerprint has to come before the download button,
// the same way `-print-pairing` puts the SPKI in front of the operator.
//
// # Measured vs published
//
// Nothing in the per-OS instructions below was performed on a device by anyone
// on this project. They are transcribed from vendor documentation and are
// labelled as such, inline, per platform. The only measurements this project
// has are the four X.509 verifiers in D101-D (Go, OpenSSL, NSS, Apple
// Security.framework on macOS) — and Chrome, Firefox, Android and iOS are NOT
// among them.
// ---------------------------------------------------------------------------

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}
function errorMessage(err: unknown, fallback: string): string {
  return (isRecord(err) && typeof err.message === 'string' ? err.message : '') || fallback
}
function str(x: unknown): string | undefined {
  return typeof x === 'string' && x ? x : undefined
}
function strList(x: unknown): string[] {
  return Array.isArray(x) ? x.filter((v): v is string => typeof v === 'string') : []
}

// RootCertInfo mirrors rootCertInfo in backend/cmd/server/lan_rootcert.go.
interface RootCertInfo {
  present: boolean
  problem?: string
  subject?: string
  notAfter?: string
  expired: boolean
  sha256?: string
  sha1?: string
  permittedDNS: string[]
  permittedIP: string[]
  pathLenZero: boolean
  downloadPath: string
  downloadURL?: string
}

function toRootCertInfo(x: unknown): RootCertInfo | null {
  if (!isRecord(x)) return null
  return {
    present: x.present === true,
    problem: str(x.problem),
    subject: str(x.subject),
    notAfter: str(x.not_after),
    expired: x.expired === true,
    sha256: str(x.sha256),
    sha1: str(x.sha1),
    permittedDNS: strList(x.permitted_dns),
    permittedIP: strList(x.permitted_ip),
    pathLenZero: x.path_len_zero === true,
    downloadPath: str(x.download_path) || '/api/lan/rootcert/download',
    downloadURL: str(x.download_url),
  }
}

// DownloadQR renders the ABSOLUTE download URL (built by the box from its LAN
// IP, never a .local name) so a phone can reach it by camera instead of by
// typing an address it may not be able to resolve. Fixed white plate for the
// same reason LANPairingPanel's does: QR contrast has to be theme-independent.
//
// Exported for its own test: the panel fetches the certificate once and never
// changes `content`, so the failure-then-recovery path below cannot be driven
// through the panel at all.
export function DownloadQR({ content }: { content: string }) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [renderError, setRenderError] = useState('')

  // The canvas stays MOUNTED while the error text is showing. It used to be
  // swapped out for the error, which left canvasRef.current null, so the guard
  // below early-returned for every subsequent `content` and the first failure
  // became permanent — and the optimistic clear that used to sit here ran
  // AFTER that guard, so it could never undo it.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!content || !canvas) return
    let cancelled = false
    QRCode.toCanvas(canvas, content, {
      width: 180,
      margin: 2,
      color: { dark: '#0a0a0a', light: '#ffffff' },
      errorCorrectionLevel: 'M',
    })
      // Cleared by a render that SUCCEEDED, not by one that was merely started.
      // While a retry is in flight the previous failure is still the last thing
      // known to be true, so the typed-address fallback stays on screen until
      // there is actually a QR code to replace it with.
      .then(() => { if (!cancelled) setRenderError('') })
      .catch((err: unknown) => { if (!cancelled) setRenderError(errorMessage(err, 'QR render failed')) })
    return () => { cancelled = true }
  }, [content])

  return (
    <>
      {renderError && (
        <div className="text-xs text-[var(--text-muted)] text-center p-4 rounded-xl border border-[var(--border-default)]">
          Could not render the QR code ({renderError}). Type the address below instead.
        </div>
      )}
      <canvas
        ref={canvasRef}
        role="img"
        aria-label="QR code linking to the root certificate download"
        className="rounded-xl mx-auto block"
        // Hidden via `display`, NOT the `hidden` attribute: `block` in the
        // className above out-specifies the UA's [hidden] rule (both are one
        // selector, author beats UA) and the canvas would stay visible.
        style={{ imageRendering: 'pixelated', background: '#ffffff', padding: 10, display: renderError ? 'none' : 'block' }}
        aria-hidden={renderError ? true : undefined}
      />
    </>
  )
}

// ── Per-OS instructions ────────────────────────────────────────────────────
//
// Each entry carries `source`, rendered verbatim in the UI. Every one of them
// currently says "published" because nobody on this project has installed this
// root on any device. If that ever changes, the entry — not the heading — is
// what gets updated, so a measured platform cannot be implied by a neighbour.

interface OSGuide {
  id: string
  label: string
  source: string
  steps: string[]
  /** The thing that most often goes wrong, stated before it does. */
  gotcha?: { tone: 'warning' | 'danger'; title: string; body: string }
}

const OS_GUIDES: OSGuide[] = [
  {
    id: 'android',
    label: 'Android',
    source: 'Published: transcribed from Android documentation. NOT performed on a device by this project — nobody here has an Android phone to test on.',
    steps: [
      'Download the file on the phone (scan the QR code above, or copy it across).',
      'Settings → Security → Encryption & credentials → Install a certificate → CA certificate.',
      'Android shows a full-screen warning here. Continue past it, then pick the downloaded file.',
    ],
    gotcha: {
      tone: 'warning',
      title: 'Two things Android will do that are not faults',
      body:
        'Android will keep telling you your network may be monitored. Once any user CA is installed it shows a standing notice, and it will not go away — user-installed CAs are most often put there by workplace monitoring tools, so Android warns unconditionally and cannot tell your constrained root apart from an interception proxy. Separately, since Android 7 apps do NOT trust user-installed CAs unless they opt in. Chrome and other browsers do. So this gets you a padlock in the browser and changes nothing for a third-party app talking to your box.',
    },
  },
  {
    id: 'ios',
    label: 'iPhone / iPad',
    source: 'Published: transcribed from Apple documentation. NOT performed on a device by this project.',
    steps: [
      'Get the file onto the device (scan the QR code above, or AirDrop/email it). iOS saves it as a profile.',
      'Settings → General → VPN & Device Management → tap the downloaded profile → Install.',
      'Settings → General → About → Certificate Trust Settings → turn ON the switch next to this certificate.',
    ],
    gotcha: {
      tone: 'danger',
      title: 'Step 3 is where almost everyone stops, and stopping there does not work',
      body:
        'Installing the profile is not enough on iOS. Until you turn on full trust under About → Certificate Trust Settings, iOS holds the certificate but does not trust it for TLS, and Safari keeps showing the warning with no error that points at the cause. iOS shows its own warning when you enable full trust; that warning is accurate — you are granting real authority — it just does not mention that this particular root is name-constrained and cannot be used against public sites.',
    },
  },
  {
    id: 'macos',
    label: 'macOS',
    source: 'Published: transcribed from Apple documentation. The related MEASUREMENT this project does have is that Apple Security.framework on macOS enforces this root’s name constraints (D101-D) — that is about the constraint, not about this install flow.',
    steps: [
      'Double-click the downloaded file — Keychain Access opens.',
      'Add it to the login keychain (or System, for every user on the Mac).',
      'Find it in the list, open it, expand Trust, and set “When using this certificate” to Always Trust.',
      'Restart the browser.',
    ],
    gotcha: {
      tone: 'warning',
      title: 'Importing is not trusting',
      body: 'On macOS a certificate can sit in the keychain and still be untrusted. If you skip the Trust → Always Trust step, nothing changes and the browser gives no hint why. Firefox needs its own step as well — see the Firefox tab.',
    },
  },
  {
    id: 'windows',
    label: 'Windows',
    source: 'Published: transcribed from Microsoft documentation. NOT performed on a machine by this project.',
    steps: [
      'Double-click the downloaded file → Install Certificate.',
      'Choose Local Machine (needs admin) or Current User.',
      'Choose “Place all certificates in the following store” → Browse → Trusted Root Certification Authorities.',
      'Restart the browser.',
    ],
    gotcha: {
      tone: 'warning',
      title: 'The default store is the wrong store',
      body: '“Automatically select the certificate store” puts a root CA somewhere it will never be consulted for TLS, and the install reports success either way. Pick Trusted Root Certification Authorities by hand. Firefox does not use the Windows store by default — see the Firefox tab.',
    },
  },
  {
    id: 'linux',
    label: 'Linux',
    source: 'Published: standard ca-certificates / p11-kit paths. NOT performed on a machine by this project.',
    steps: [
      'Debian/Ubuntu: copy the file to /usr/local/share/ca-certificates/vulos-root.crt then run sudo update-ca-certificates.',
      'Fedora/RHEL: copy it to /etc/pki/ca-trust/source/anchors/ then run sudo update-ca-trust.',
      'Restart the browser.',
    ],
    gotcha: {
      tone: 'warning',
      title: 'Chrome on Linux may not read the system store',
      body: 'Chrome and Chromium on Linux have historically used their own NSS database rather than the system anchors, in which case the system-wide install above has no effect on the browser and you need certutil against ~/.pki/nssdb. Firefox always keeps its own store — see the Firefox tab.',
    },
  },
  {
    id: 'firefox',
    label: 'Firefox (any OS)',
    source: 'Published: transcribed from Mozilla documentation. NOT performed by this project. Note that Firefox uses mozilla::pkix, which is NOT the NSS verifier D101-D measured — Firefox is on the unmeasured list.',
    steps: [
      'Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import.',
      'Tick “Trust this CA to identify websites”.',
      'Or instead: set security.enterprise_roots.enabled to true in about:config to make Firefox read the OS store.',
    ],
    gotcha: {
      tone: 'warning',
      title: 'Firefox ignores the OS trust store by default',
      body: 'On Windows, macOS and Linux alike, installing the root system-wide does nothing for Firefox until you do one of the two steps above. This is the single most common reason “I installed it and it still warns me”.',
    },
  },
]

export default function LANRootCertPanel() {
  const [info, setInfo] = useState<RootCertInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [os, setOS] = useState<string>('android')
  const [copied, setCopied] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    fetch('/api/lan/rootcert', { credentials: 'include' })
      .then(async r => {
        const raw: unknown = await r.json().catch(() => null)
        if (!r.ok) {
          throw new Error((isRecord(raw) && typeof raw.error === 'string' && raw.error) || `HTTP ${r.status}`)
        }
        const parsed = toRootCertInfo(raw)
        if (!parsed) throw new Error('The box did not return a usable answer.')
        setInfo(parsed)
      })
      .catch((err: unknown) => { setInfo(null); setError(errorMessage(err, 'Could not ask the box about its root certificate.')) })
      .finally(() => setLoading(false))
  }, [])

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { load() }, [load])

  const copyFingerprint = useCallback(() => {
    const value = info?.sha256
    if (!value) return
    const clip = typeof navigator !== 'undefined' ? navigator.clipboard : undefined
    if (!clip?.writeText) return
    clip.writeText(value).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }).catch(() => { /* a failed copy leaves the value on screen to read */ })
  }, [info])

  const guide = OS_GUIDES.find(g => g.id === os) || OS_GUIDES[0]

  return (
    <Section
      title="Browser Trust"
      desc="Install this box's certificate authority on a device so its LAN address shows a normal padlock in a normal browser, with no internet connection."
    >
      <Banner tone="info" title="What this is for, and what it costs">
        A browser will only show a padlock for <code>vulos.local</code> if the device trusts whoever
        signed the box&rsquo;s certificate. No public authority may ever sign for <code>.local</code>
        &nbsp;&mdash; that is a prohibition, not a gap &mdash; so the only route is a certificate authority
        you installed yourself. <strong>One install per device, nothing recurring.</strong> If you skip
        it, the box still works and still encrypts; you just click through one warning per browser.
        Native clients need none of this &mdash; see Native Pairing, which pins the box&rsquo;s own key
        instead.
      </Banner>

      {loading && (
        <Card><p className="text-sm text-[var(--text-tertiary)]">Asking the box&hellip;</p></Card>
      )}

      {!loading && error && (
        <Banner tone="danger" title="Could not reach the box">{error}</Banner>
      )}

      {!loading && !error && info && info.problem && (
        <Banner tone="danger" title="This box is refusing to hand out the certificate it has">
          {info.problem}
        </Banner>
      )}

      {!loading && !error && info && !info.present && !info.problem && (
        <Card title="No certificate authority has reached this box yet">
          <EmptyState
            icon="about"
            title="Nothing to install yet"
            hint="This is a normal state, not a fault — the authority is deliberately run on your own computer, never on the box."
          />
          <div className="text-sm text-[var(--text-secondary)] leading-relaxed space-y-3">
            <p>On your own computer, once:</p>
            <pre className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] p-3 text-xs overflow-x-auto"><code>{'vulos-lanca init -label "home"\nvulos-lanca root > vulos-root.crt'}</code></pre>
            <p>
              Then get <code>vulos-root.crt</code> onto this box at
              {' '}<code>/var/lib/vulos/tls/lan-root.crt</code>, and this page will offer it to every
              device in the house. The authority&rsquo;s private key must stay on your computer &mdash;
              the tool refuses to write it to a box path.
            </p>
          </div>
        </Card>
      )}

      {!loading && !error && info && info.present && (
        <>
          {info.expired && (
            <Banner tone="warning" title="This authority has expired">
              It will still install on most devices and will validate nothing. Re-run
              {' '}<code>vulos-lanca init</code> in a fresh directory on your computer and re-issue.
              Your box stays reachable either way &mdash; it falls back to its self-signed
              certificate and the one-click warning.
            </Banner>
          )}

          <Banner tone="warning" title="Check the fingerprint before you install this">
            You are about to download this file over a connection <strong>your device does not trust
            yet</strong> &mdash; that is the whole reason you are here, and it means this first fetch
            is not authenticated. Anyone able to intercept your network could serve you a different
            authority. Compare the fingerprint below against the machine that actually holds the
            authority, over a channel that is not this one, before you install it. This is the same
            check Native Pairing asks you to make of the box&rsquo;s key fingerprint.
          </Banner>

          <Card
            title="1 — The fingerprint to verify"
            desc="SHA-256 of the certificate. This is the value to compare; do not rely on the name."
          >
            <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] p-3">
              <code
                data-testid="root-sha256"
                className="block font-mono text-[11px] leading-relaxed break-all text-[var(--text-primary)]"
              >
                {info.sha256}
              </code>
            </div>
            <div className="mt-3 flex flex-wrap items-center gap-2">
              <button type="button" className="btn text-sm" onClick={copyFingerprint}>
                {copied ? 'Copied' : 'Copy fingerprint'}
              </button>
            </div>
            <p className="mt-4 text-sm text-[var(--text-secondary)] leading-relaxed">
              On the computer that holds the authority, either of these prints the same value:
            </p>
            <pre className="mt-2 rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)] p-3 text-xs overflow-x-auto"><code>{'vulos-lanca inspect ~/.vulos-lanca/root.crt\nopenssl x509 -in ~/.vulos-lanca/root.crt -noout -fingerprint -sha256'}</code></pre>
            <InfoList className="mt-4">
              <InfoRow label="Name on the certificate" value={info.subject} />
              <InfoRow label="Expires" value={info.notAfter} />
              {info.sha1 && <InfoRow label="SHA-1 (only for dialogs that show nothing else)" value={info.sha1} mono />}
            </InfoList>
            {info.sha1 && (
              <p className="mt-2 text-xs text-[var(--text-muted)] leading-relaxed">
                SHA-1 is here because some certificate dialogs still show only that. It is a weaker
                digest &mdash; when a device offers you SHA-256, compare SHA-256.
              </p>
            )}
          </Card>

          <Card
            title="2 — What this authority is allowed to vouch for"
            desc="Read off the certificate itself, not from a description of it."
          >
            <InfoList>
              <InfoRow label="Permitted names" value={info.permittedDNS.join(', ') || 'none listed'} mono />
              <InfoRow label="Permitted addresses" value={info.permittedIP.join(', ') || 'none listed'} mono />
              <InfoRow
                label="Can it create further authorities?"
                value={info.pathLenZero ? 'No — path length 0' : 'Yes — no path-length limit'}
                ok={info.pathLenZero}
              />
            </InfoList>
            <p className="mt-3 text-sm text-[var(--text-secondary)] leading-relaxed">
              None of those names resolve on the public internet. If this authority&rsquo;s private key
              were stolen, it still could not produce a working certificate for a public site &mdash; which
              is what makes installing it a much smaller thing than the roughly 150 unrestricted
              authorities your device already trusts.
            </p>
            <p data-testid="constraint-measured" className="mt-2 text-xs text-[var(--text-muted)] leading-relaxed">
              That protection only holds where the software enforces name constraints. Enforcement was
              measured on Go&rsquo;s crypto/x509, OpenSSL, NSS and Apple&rsquo;s Security.framework on macOS.
              It has <strong>not</strong> been measured on Chrome, Firefox, Android or iOS.
            </p>
          </Card>

          <Card
            title="3 — Get the file onto the device"
            desc="On this device use the button. On a phone, scan the code."
          >
            <div className="flex flex-wrap items-start gap-6">
              <div className="min-w-[14rem] flex-1">
                <a
                  className="btn text-sm inline-flex"
                  href={info.downloadPath}
                  download="vulos-root.crt"
                >
                  Download the certificate
                </a>
                <p className="mt-3 text-sm text-[var(--text-secondary)] leading-relaxed">
                  The file is public and contains no secret &mdash; it is a certificate, not a key.
                  Copying it around by AirDrop, USB or email is fine and avoids the untrusted first
                  fetch entirely.
                </p>
              </div>
              {info.downloadURL && (
                <div className="text-center">
                  <DownloadQR content={info.downloadURL} />
                  <code className="mt-2 block font-mono text-[10px] break-all text-[var(--text-muted)] max-w-[14rem]">
                    {info.downloadURL}
                  </code>
                </div>
              )}
            </div>
            {info.downloadURL && (
              <Banner tone="info" title="The phone has to be signed in to this box first">
                That address is behind the same sign-in as the rest of Settings, so a phone that has
                never signed in will be asked to. Open the box in the phone&rsquo;s browser, accept the
                one-click certificate warning (the one you are here to get rid of), sign in, and then
                scan. The address deliberately uses the box&rsquo;s IP rather than a
                {' '}<code>.local</code> name, because a phone that cannot resolve
                {' '}<code>.local</code> is exactly the case this whole flow exists for.
              </Banner>
            )}
          </Card>

          <Card
            title="4 — Install it"
            desc="The steps differ by platform, and two of them have a step people reliably miss."
          >
            <div role="tablist" aria-label="Operating system" className="flex flex-wrap gap-2">
              {OS_GUIDES.map(g => (
                <button
                  key={g.id}
                  type="button"
                  role="tab"
                  aria-selected={g.id === os}
                  className={g.id === os ? 'btn text-sm' : 'btn-ghost text-sm'}
                  onClick={() => setOS(g.id)}
                >
                  {g.label}
                </button>
              ))}
            </div>

            <div className="mt-4" role="tabpanel" aria-label={guide.label}>
              <ol className="list-decimal ml-5 space-y-2 text-sm text-[var(--text-secondary)] leading-relaxed">
                {guide.steps.map((s, i) => <li key={i}>{s}</li>)}
              </ol>

              {guide.gotcha && (
                <div className="mt-4">
                  <Banner tone={guide.gotcha.tone} title={guide.gotcha.title}>
                    {guide.gotcha.body}
                  </Banner>
                </div>
              )}

              <p data-testid="os-source" className="mt-4 text-xs text-[var(--text-muted)] leading-relaxed">
                {guide.source}
              </p>
            </div>
          </Card>

          <Card title="Changed your mind?">
            <p className="text-sm text-[var(--text-secondary)] leading-relaxed">
              Delete it from the device&rsquo;s certificate store. Your box keeps working &mdash; it falls
              back to its self-signed certificate and the one-click warning comes back. The same thing
              happens if the box&rsquo;s certificate expires: an expired certificate never locks you out
              of your own box.
            </p>
          </Card>
        </>
      )}
    </Section>
  )
}
