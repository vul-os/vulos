import { describe, it, expect } from 'vitest'
import {
  WRITER_TIMEOUT_MS,
  decideRole,
  livePeersOn,
  nextWriter,
  suggestDesktopId,
  type PeerInfo,
} from '../providers/shellSession'

// The bug these exist to prevent, stated plainly:
//
// Shell state persists to ONE localStorage key on a 500ms debounce, and every
// tab kept its own independent in-memory copy. With no cross-tab coordination
// at all, two tabs were last-writer-wins against each other — open a window in
// tab A, move one in tab B, and B's save dropped A's window. Nothing threw and
// nothing was shown; on refresh you got whichever tab wrote last.
//
// So the load-bearing property is: EXACTLY ONE writer per desktop, and every
// tab independently reaches the same answer about who it is. Two tabs both
// believing they are the writer restores the original bug.

const T0 = 1_000_000

function peer(tabId: string, desktopId: string, role: 'writer' | 'follower', lastSeen: number): PeerInfo {
  return { tabId, desktopId, role, lastSeen }
}

describe('decideRole — exactly one writer per desktop', () => {
  it('claims writer when nobody holds the desktop', () => {
    expect(decideRole([], 'desktop-1', T0)).toBe('writer')
  })

  it('follows a live writer rather than duelling with it', () => {
    const peers = [peer('a', 'desktop-1', 'writer', T0 - 100)]
    expect(decideRole(peers, 'desktop-1', T0)).toBe('follower')
  })

  it('claims writer when the existing writer has gone silent past the timeout', () => {
    // A crashed or force-closed tab never sends 'bye'. Without this the desktop
    // would be left with no writer and stop persisting entirely.
    const peers = [peer('a', 'desktop-1', 'writer', T0 - WRITER_TIMEOUT_MS - 1)]
    expect(decideRole(peers, 'desktop-1', T0)).toBe('writer')
  })

  it('is unaffected by a writer on a DIFFERENT desktop', () => {
    // This is the whole point of offering a newcomer its own desktop: two tabs
    // on separate desktops must BOTH be writers, or the second one silently
    // stops persisting.
    const peers = [peer('a', 'desktop-1', 'writer', T0 - 100)]
    expect(decideRole(peers, 'desktop-2', T0)).toBe('writer')
  })

  it('ignores a live FOLLOWER — a follower does not own the key', () => {
    const peers = [peer('a', 'desktop-1', 'follower', T0 - 100)]
    expect(decideRole(peers, 'desktop-1', T0)).toBe('writer')
  })
})

describe('nextWriter — successor is deterministic, so exactly one promotes', () => {
  it('every remaining tab computes the SAME successor', () => {
    const peers = [
      peer('c', 'desktop-1', 'follower', T0),
      peer('a', 'desktop-1', 'follower', T0),
      peer('b', 'desktop-1', 'follower', T0),
    ]
    // Each tab runs this against its own peer set; agreement is what stops two
    // tabs promoting themselves at once and reinstating the duelling writers.
    expect(nextWriter(peers, 'desktop-1', T0)).toBe('a')
    expect(nextWriter([...peers].reverse(), 'desktop-1', T0)).toBe('a')
  })

  it('skips tabs that have themselves gone silent', () => {
    const peers = [
      peer('a', 'desktop-1', 'follower', T0 - WRITER_TIMEOUT_MS - 1),
      peer('b', 'desktop-1', 'follower', T0),
    ]
    expect(nextWriter(peers, 'desktop-1', T0)).toBe('b')
  })

  it('returns null when nobody is left', () => {
    expect(nextWriter([], 'desktop-1', T0)).toBeNull()
  })

  it('does not promote a tab from another desktop', () => {
    const peers = [peer('a', 'desktop-2', 'follower', T0)]
    expect(nextWriter(peers, 'desktop-1', T0)).toBeNull()
  })
})

describe('livePeersOn — what the share prompt is allowed to react to', () => {
  it('excludes self', () => {
    const peers = [peer('me', 'desktop-1', 'writer', T0), peer('other', 'desktop-1', 'follower', T0)]
    expect(livePeersOn(peers, 'desktop-1', 'me', T0).map(p => p.tabId)).toEqual(['other'])
  })

  it('excludes stale peers, so a tab closed long ago cannot trigger the prompt', () => {
    const peers = [peer('ghost', 'desktop-1', 'writer', T0 - WRITER_TIMEOUT_MS - 1)]
    expect(livePeersOn(peers, 'desktop-1', 'me', T0)).toEqual([])
  })

  it('excludes peers on other desktops', () => {
    const peers = [peer('other', 'desktop-2', 'writer', T0)]
    expect(livePeersOn(peers, 'desktop-1', 'me', T0)).toEqual([])
  })
})

describe('suggestDesktopId', () => {
  it('proposes the next unused id', () => {
    expect(suggestDesktopId(['desktop-1'])).toBe('desktop-2')
  })

  it('skips ids already taken rather than colliding', () => {
    // Colliding would drop the newcomer onto an EXISTING desktop — the opposite
    // of giving them their own.
    expect(suggestDesktopId(['desktop-1', 'desktop-2'])).toBe('desktop-3')
    expect(suggestDesktopId(['desktop-2'])).not.toBe('desktop-2')
  })
})
