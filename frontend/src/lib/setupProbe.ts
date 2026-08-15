/**
 * setupProbe.ts — "has this box been set up?", asked so that a bad answer is
 * never mistaken for a good one.
 *
 * The shell asks GET /api/setup/status exactly once at boot, and the answer
 * decides whether the fifteen-step first-boot wizard runs. App.tsx used to do
 * it like this:
 *
 *     .then(r => r.ok ? r.json() : { setup_complete: true })
 *     .catch(() => setSetupDone(true))
 *
 * — one failed or slow probe, and the box is treated as already set up. That is
 * fail-open on the one screen where failing open means the machine never gets
 * set up at all, and the moment it is most likely to happen is a box that is
 * still starting: exactly when a FIRST boot happens. The kiosk waits for this
 * endpoint before launching a browser (scripts/vulos-kiosk.sh), but a browser
 * opened by hand from the LAN, or a reload during a restart, has no such wait.
 *
 * So: retry, and when the answer is still not known, SAY so. This function
 * never guesses. It returns a boolean only when the box actually answered with
 * one; otherwise 'unknown', and the caller has to decide what to do about that
 * (App.tsx asks the user).
 *
 * Retry budget: 4 attempts at 0 / 0.5 / 1.5 / 3.5 s — about 5.5 s of waiting,
 * plus up to 2 s per attempt before it is abandoned. Long enough to cover a
 * backend that is still coming up, short enough that nobody watches a spinner
 * wondering whether the machine is dead.
 */

export type SetupProbeResult = boolean | 'unknown'

/** Delay BEFORE each attempt, in ms. Its length is the attempt count. */
export const PROBE_DELAYS_MS = [0, 500, 1500, 3500]

/** How long a single attempt may take before it is abandoned and retried. */
export const PROBE_ATTEMPT_TIMEOUT_MS = 2000

export interface ProbeOptions {
  /** Injected for tests; defaults to the global fetch. */
  fetchImpl?: typeof fetch
  /** Injected for tests; defaults to a real timer. */
  sleep?: (ms: number) => Promise<void>
  delays?: number[]
  timeoutMs?: number
}

const realSleep = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

/**
 * One attempt. Returns a boolean when the box gave a usable answer, or null
 * when this attempt told us nothing (network error, timeout, non-2xx, or a 200
 * whose body is not the shape this endpoint has — which means something other
 * than the box, e.g. a captive portal or a proxy, answered).
 */
async function attempt(fetchImpl: typeof fetch, timeoutMs: number): Promise<boolean | null> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    const res = await fetchImpl('/api/setup/status', { signal: controller.signal })
    if (!res.ok) return null
    const data: unknown = await res.json()
    if (!isRecord(data) || typeof data.setup_complete !== 'boolean') return null
    return data.setup_complete
  } catch {
    return null
  } finally {
    clearTimeout(timer)
  }
}

/**
 * Ask the box whether first-boot setup is finished.
 *
 * Resolves to the box's own answer, or 'unknown' if every attempt failed. It
 * never resolves to a value the box did not give: an unreachable box is
 * 'unknown', not `true`, and not `false` either — guessing "not set up" would
 * be its own defect, because re-running the wizard on a box that IS set up
 * fails at the account step on a duplicate username.
 */
export async function probeSetupComplete(opts: ProbeOptions = {}): Promise<SetupProbeResult> {
  const fetchImpl = opts.fetchImpl ?? ((...args: Parameters<typeof fetch>) => fetch(...args))
  const sleep = opts.sleep ?? realSleep
  const delays = opts.delays ?? PROBE_DELAYS_MS
  const timeoutMs = opts.timeoutMs ?? PROBE_ATTEMPT_TIMEOUT_MS

  for (const delay of delays) {
    if (delay > 0) await sleep(delay)
    const answer = await attempt(fetchImpl, timeoutMs)
    if (answer !== null) return answer
  }
  return 'unknown'
}
