/**
 * bootmode.ts — the shell's mirror of GET /api/setup/mode.
 *
 * This endpoint reports the BOX'S OWN instance-identity and replication state.
 * It does not answer "has the owner finished first-run setup?" — GET
 * /api/setup/status does, and AuthGate has already asked it before anything
 * that imports this file is on screen (App.tsx / lib/setupProbe.ts).
 *
 * The distinction is not academic. The three modes used to be named
 * `setup` / `sync` / `normal`, and Setup.tsx read `normal` as "already set up"
 * and dismissed itself:
 *
 *     if (data.mode === 'normal') {
 *       // Already set up — shouldn't be here, but complete gracefully
 *       onComplete()
 *     }
 *
 * `normal` meant "db/instance.json exists and nothing is syncing". The server
 * writes instance.json at STARTUP (registerIdentityRoutes → identity.Load), so
 * it was already true on a box nobody had ever touched. The fifteen-step wizard
 * therefore mounted, asked one question of the box, and handed the founder a
 * "Create your account" login form on a box with no accounts — the exact
 * symptom a backend fix had just been shipped to kill.
 *
 * So: the only question this endpoint can honestly answer is the one below.
 * Everything else about it is state that no first-run decision may depend on.
 *
 * The strings are the Go package's, checked against
 * backend/services/bootmode/bootmode.go by TestModeStringsMatchFrontend — a
 * value added on one side and not the other fails the Go build's test run.
 */

/** No db dir or no db/instance.json yet. Unreachable over HTTP: the server has
 *  written instance.json before it accepts a connection. Listed because the
 *  endpoint's contract includes it, not because a browser can see it. */
export const MODE_INSTANCE_ABSENT = 'instance_absent'

/** db/sync-state.json says "syncing": this box is replicating from a cluster. */
export const MODE_SYNCING = 'syncing'

/** Instance identity on disk, nothing replicating. TRUE ON A PRISTINE FIRST
 *  BOOT. Says nothing about accounts, ownership, or setup. */
export const MODE_INSTANCE_READY = 'instance_ready'

/** Every value the endpoint can emit. */
export const INSTANCE_MODES = [
  MODE_INSTANCE_ABSENT,
  MODE_SYNCING,
  MODE_INSTANCE_READY,
] as const

export type InstanceMode = (typeof INSTANCE_MODES)[number]

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

/**
 * Is this box currently replicating from a cluster it is joining?
 *
 * The ONLY thing a caller may conclude from /api/setup/mode. Anything that is
 * not an explicit `syncing` — a different mode, a malformed body, a captive
 * portal's HTML, a 500, an unreachable box — is `false`, i.e. "carry on with
 * the normal flow". That default is deliberate and it is the safe one: the
 * normal flow is the fifteen-step wizard, which the caller only reached because
 * /api/setup/status said setup was outstanding.
 */
export function isJoiningCluster(data: unknown): boolean {
  return isRecord(data) && data.mode === MODE_SYNCING
}
