// shellSession.ts — make a second tab or a second client a supported thing
// rather than a silent corruption.
//
// # What was wrong
//
// Shell state persists to ONE localStorage key on a 500ms debounce, and every
// tab on the origin keeps its own independent in-memory copy. There was no
// `storage` listener and no BroadcastChannel anywhere in the frontend, so two
// tabs were last-writer-wins against each other: open a window in tab A, move
// one in tab B, and B's next save wipes A's window. Nothing threw. On refresh
// you got whichever tab happened to write last.
//
// Silent state loss is worse than a visible failure — the user has no way to
// tell it happened, and no reason to distrust what they are looking at.
//
// # What this does instead
//
// Tabs announce themselves on a BroadcastChannel and elect exactly one WRITER
// per desktop. The writer owns the localStorage key for that desktop; every
// other tab on the same desktop is a FOLLOWER and mirrors the writer's state
// instead of racing it. So sharing one desktop across two tabs is genuinely
// supported: both show the same windows, and neither clobbers the other.
//
// A tab that would otherwise become a follower is OFFERED ITS OWN DESKTOP —
// encouraged, not forced. That is the better default (two people, or two
// screens, usually want separate workspaces) but "watch the same desktop from
// two places" is a real thing people do, so it stays available and works.
//
// # Why election rather than merging
//
// The obvious alternative is to merge both tabs' state. But window layout is
// not commutative — two tabs each moving the same window to different places
// have no correct merge, and picking one silently is the bug we are fixing.
// Election has an answer a user can predict: the tab that claimed the desktop
// is the one driving it, and the others follow.
//
// BroadcastChannel is same-origin and same-browser. A second CLIENT (another
// device, another browser) cannot be seen this way — that is a genuine limit,
// stated here rather than implied away, and it is why the desktop's identity is
// also written to the persisted state so a returning client can be told what it
// is joining.
//
// # A display disappearing is not the same event as a tab closing
//
// Every promotion path above (decideRole's timeout branch, nextWriter after a
// `bye`) assumes the old writer is actually gone: closed, crashed, or silent
// forever. A tab that closes fires `pagehide`, which sends `bye`, and then it
// stops existing — there is no way for it to come back and disagree with
// whoever took over.
//
// A monitor being unplugged is a weaker event than that. In the one-browser-
// per-output model (see roadmap/SCREENS.md), the browser instance driving that
// output does not necessarily quit when the output goes away — that depends on
// the compositor and the launcher, and per SCREENS.md is not yet verified
// against real hardware. What IS certain, because it follows from ordinary
// browser scheduling rather than from any compositor behaviour, is that a tab
// which is not painting to a live surface is exactly the kind of tab browsers
// throttle: background timers can be delayed far past WRITER_TIMEOUT_MS and
// then resume. So the sequence this module has to survive is not "writer dies,
// successor elected" but "writer goes silent past the timeout, a follower
// promotes itself, and THEN the original writer's throttled heartbeat fires
// again" — two tabs both convinced they are the writer for the same desktop,
// neither having sent `bye`. That is the exact duelling-writer bug this module
// exists to prevent, entered from a direction the original design (promotion
// only, no demotion) had no way to close: nothing ever told an established
// writer to stop being one.
//
// shouldStepDown below is that missing direction. It is symmetric with
// nextWriter's tie-break (lowest tabId) rather than inventing a second rule,
// so a resurrected writer and its successor reach the same conclusion about
// which of them survives without a negotiation round — same as promotion, each
// tab decides alone from its own view of the peer set and every honest tab
// decides the same way. It only compares tabIds that are THEMSELVES live
// writers right now, never "would a newcomer with a lower id become writer if
// elected fresh" (that's nextWriter's job, and reusing it here would make a
// stable writer demote itself just because a new follower with a lower tabId
// joined — churn with no conflict behind it).

export const SHELL_CHANNEL = 'vulos-shell-session'

// How long a writer may go silent before a follower assumes it is gone. Two
// heartbeat intervals plus slack: long enough that a busy main thread or a
// backgrounded tab is not evicted, short enough that closing the writer hands
// over while the user is still looking at the screen.
export const HEARTBEAT_MS = 2000
export const WRITER_TIMEOUT_MS = 5000

export type SessionRole = 'writer' | 'follower'

export interface PeerInfo {
  /** Stable per-tab id — NOT the user id; two tabs of one user are two peers. */
  tabId: string
  desktopId: string
  role: SessionRole
  /** Wall-clock ms of the last message seen from this peer. */
  lastSeen: number
}

export type SessionMessage =
  | { kind: 'hello'; tabId: string; desktopId: string; role: SessionRole; at: number }
  | { kind: 'heartbeat'; tabId: string; desktopId: string; role: SessionRole; at: number }
  | { kind: 'bye'; tabId: string; desktopId: string; at: number }
  /** Writer → followers: the state they should mirror. */
  | { kind: 'state'; tabId: string; desktopId: string; state: unknown; at: number }
  /** Follower → writer: "you drive, but do this" (a click in a follower tab). */
  | { kind: 'intent'; tabId: string; desktopId: string; action: unknown; at: number }

/**
 * newTabId returns a per-tab identifier.
 *
 * crypto.randomUUID is not available on every browser/context this shell runs
 * in (notably non-secure origins, which a LAN box can legitimately be), so it
 * is used when present and a random fallback otherwise. This is an identifier,
 * never a secret — nothing authenticates with it — so the fallback's weaker
 * randomness is not a security property.
 */
export function newTabId(): string {
  const c = globalThis.crypto as Crypto | undefined
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  return `tab-${Math.random().toString(36).slice(2)}${Date.now().toString(36)}`
}

/**
 * decideRole determines what a joining tab should be.
 *
 * A tab becomes the WRITER when no live writer already holds its desktop.
 * Otherwise it is a follower — and the caller is expected to OFFER it its own
 * desktop rather than silently making it a passenger.
 *
 * `now` is injected so this is testable without faking timers, and so a caller
 * can evaluate a peer set captured at a known instant.
 */
export function decideRole(peers: PeerInfo[], desktopId: string, now: number): SessionRole {
  const liveWriter = peers.some(
    p => p.desktopId === desktopId && p.role === 'writer' && now - p.lastSeen < WRITER_TIMEOUT_MS,
  )
  return liveWriter ? 'follower' : 'writer'
}

/**
 * livePeersOn returns the peers currently on a desktop, excluding self and
 * anything that has gone quiet past the timeout.
 *
 * Used to decide whether to show the "another window is using this desktop"
 * prompt at all — a stale entry from a tab closed an hour ago must not trigger
 * it, which is the difference between a helpful prompt and an annoying one.
 */
export function livePeersOn(peers: PeerInfo[], desktopId: string, selfTabId: string, now: number): PeerInfo[] {
  return peers.filter(
    p => p.desktopId === desktopId && p.tabId !== selfTabId && now - p.lastSeen < WRITER_TIMEOUT_MS,
  )
}

/**
 * nextWriter picks the successor when a writer disappears.
 *
 * Deterministic by lowest tabId among live followers on that desktop, so every
 * remaining tab independently reaches the SAME answer without needing a round
 * of negotiation. Two tabs both promoting themselves would restore exactly the
 * duelling-writer bug this module exists to remove.
 *
 * Returns null when nobody is left to take over.
 */
export function nextWriter(peers: PeerInfo[], desktopId: string, now: number): string | null {
  const candidates = peers
    .filter(p => p.desktopId === desktopId && now - p.lastSeen < WRITER_TIMEOUT_MS)
    .map(p => p.tabId)
    .sort()
  return candidates.length > 0 ? candidates[0] : null
}

/**
 * shouldStepDown decides whether a tab that currently believes itself the
 * writer must demote to follower because another live tab is ALSO claiming
 * writer for the same desktop right now.
 *
 * This is the other half of the election `decideRole`/`nextWriter` handle.
 * Those answer "who becomes writer when none exists" — a question a closed
 * tab can only ever ask once, since closing is terminal. This answers "what
 * happens when two already believe they are" — a question a tab whose
 * display was unplugged but whose process kept running can ask more than
 * once, if a throttled heartbeat resumes after a successor was already
 * elected. See the module comment above for why that is a real sequence and
 * not a hypothetical one.
 *
 * Resolved with the SAME tie-break `nextWriter` uses (lowest tabId), so the
 * two conflicting tabs need no negotiation: each independently compares
 * itself only to other peers presently claiming 'writer', and the one with
 * the higher id steps down. A peer merely existing on the desktop — a
 * follower, or a newcomer who has not yet decided its own role — is not a
 * conflict and must not cause a step-down; only a peer that is ITSELF
 * claiming 'writer' counts.
 */
export function shouldStepDown(selfTabId: string, peers: PeerInfo[], desktopId: string, now: number): boolean {
  return peers.some(
    p =>
      p.desktopId === desktopId &&
      p.tabId !== selfTabId &&
      p.role === 'writer' &&
      p.tabId < selfTabId &&
      now - p.lastSeen < WRITER_TIMEOUT_MS,
  )
}

/**
 * suggestDesktopId proposes an id for the "give me my own desktop" path.
 *
 * Deliberately derived from the existing set rather than random, so the label
 * a user sees ("Desktop 2") matches what the shell already calls them.
 */
export function suggestDesktopId(existingIds: string[]): string {
  let n = existingIds.length + 1
  const taken = new Set(existingIds)
  while (taken.has(`desktop-${n}`)) n++
  return `desktop-${n}`
}

/** Minimal surface of BroadcastChannel we use, so tests can substitute one. */
export interface Channel {
  postMessage(msg: SessionMessage): void
  close(): void
  onmessage: ((ev: { data: SessionMessage }) => void) | null
}

/**
 * openChannel returns a BroadcastChannel, or null where it is unavailable.
 *
 * Returning null rather than throwing matters: BroadcastChannel is absent in
 * some embedded webviews and in jsdom without a polyfill. A shell that refused
 * to start there would be a far worse regression than the co-ordination it
 * provides, so every caller treats null as "single-tab, behave as before".
 */
export function openChannel(name: string = SHELL_CHANNEL): Channel | null {
  const BC = (globalThis as { BroadcastChannel?: new (n: string) => Channel }).BroadcastChannel
  if (!BC) return null
  try {
    return new BC(name)
  } catch {
    return null
  }
}
