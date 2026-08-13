// useShellSession.ts — the React half of the cross-tab shell session.
//
// Announces this tab, tracks its peers, elects a single writer per desktop,
// steps down if it turns out another tab already holds that role, and tells
// the shell whether it owns the persisted state or is mirroring someone
// else's. The rules themselves live in shellSession.ts as pure functions so
// they can be tested without a browser, a timer or a second tab.
//
// The step-down path (shouldStepDown) exists for the case a tab closing can
// never produce: a writer that goes quiet past WRITER_TIMEOUT_MS, a follower
// promoting itself in response, and then the ORIGINAL writer's heartbeat
// resuming — plausible for an unplugged-but-not-quit browser instance, whose
// timers a background tab throttles rather than a compositor cleanly killing.
// See the "display disappearing" section of shellSession.ts for the full
// argument.
//
// Everything degrades to "single tab, behave exactly as before" when
// BroadcastChannel is unavailable — see openChannel.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  HEARTBEAT_MS,
  decideRole,
  livePeersOn,
  newTabId,
  nextWriter,
  openChannel,
  shouldStepDown,
  type Channel,
  type PeerInfo,
  type SessionMessage,
  type SessionRole,
} from './shellSession'

export interface ShellSession {
  tabId: string
  role: SessionRole
  /** Live peers on this desktop, excluding self. Drives the share prompt. */
  peers: PeerInfo[]
  /** The writer's latest state, when this tab is a follower. */
  mirrored: unknown | null
  /** Writer-only: broadcast the state followers should mirror. */
  publish: (state: unknown) => void
}

export function useShellSession(desktopId: string): ShellSession {
  // Inline arrow, not a bare `newTabId` reference: useMemo's first argument is
  // the factory, so passing the function itself reads as "memoize this value"
  // to anyone skimming, and the lint rule cannot tell the two apart.
  const tabId = useMemo(() => newTabId(), [])
  const chanRef = useRef<Channel | null>(null)
  const peersRef = useRef<Map<string, PeerInfo>>(new Map())
  const roleRef = useRef<SessionRole>('writer')

  const [role, setRole] = useState<SessionRole>('writer')
  const [peers, setPeers] = useState<PeerInfo[]>([])
  const [mirrored, setMirrored] = useState<unknown | null>(null)

  // Keep a ref alongside the state: the message handler and the heartbeat both
  // need the CURRENT role, and reading it from state would capture whatever it
  // was when the effect last ran — which for a long-lived channel handler is
  // the value at mount, i.e. always 'writer'. That would make every tab claim
  // ownership and reproduce the duelling-writer bug.
  useEffect(() => { roleRef.current = role }, [role])

  const publish = useCallback((state: unknown) => {
    const ch = chanRef.current
    if (!ch || roleRef.current !== 'writer') return
    ch.postMessage({ kind: 'state', tabId, desktopId, state, at: Date.now() })
  }, [tabId, desktopId])

  useEffect(() => {
    const ch = openChannel()
    chanRef.current = ch
    // No BroadcastChannel: this tab is alone as far as it can tell, and behaves
    // exactly as the shell did before any of this existed.
    //
    // No setRole here. 'writer' is the initial state, and every path that could
    // make this tab a follower — the message handler, the settle timeout, the
    // heartbeat — lives past this return and needs `ch`. So with no channel the
    // role has never been anything else, and setting it would only schedule a
    // render to replace a value with itself.
    if (!ch) return

    const note = (m: { tabId: string; desktopId: string; role?: SessionRole }) => {
      peersRef.current.set(m.tabId, {
        tabId: m.tabId,
        desktopId: m.desktopId,
        role: m.role ?? 'follower',
        lastSeen: Date.now(),
      })
    }

    // Change role AND say so immediately, rather than waiting for the next
    // periodic heartbeat (up to HEARTBEAT_MS away) to mention it. This matters
    // most for a step-down: the whole point of shouldStepDown is closing the
    // window where two tabs both believe they are the writer, and leaving the
    // demotion unannounced would just re-open it under a different name.
    const setRoleAndAnnounce = (r: SessionRole) => {
      setRole(r)
      ch.postMessage({ kind: 'heartbeat', tabId, desktopId, role: r, at: Date.now() })
    }

    ch.onmessage = (ev) => {
      const m = ev.data as SessionMessage
      if (!m || m.tabId === tabId) return

      switch (m.kind) {
        case 'hello':
          // A `hello` sender has not decided its own role yet — settling takes
          // another 250ms after this — so its self-announced role (always
          // 'writer', the pre-decision default) is not a real claim. Recording
          // it as one would make an established writer see every newcomer's
          // hello as a rival and step down before the newcomer has even had a
          // chance to see US and correctly settle on 'follower'.
          note({ ...m, role: 'follower' })
          // Answer a newcomer immediately rather than making it wait a full
          // heartbeat to discover us — otherwise a second tab opening next to a
          // live writer sees an empty peer set, decides it is the writer, and
          // both save until the next tick.
          ch.postMessage({ kind: 'heartbeat', tabId, desktopId, role: roleRef.current, at: Date.now() })
          break
        case 'heartbeat':
          note(m)
          // The peer we just heard from claims 'writer'. If we ALSO believe we
          // are the writer for this desktop, that is a live conflict — most
          // plausibly a display that dropped out without the tab behind it
          // ever closing, so no `bye` ever ran and this tab's own promotion
          // path never fired for the other side. Resolved deterministically
          // (see shouldStepDown) rather than by whoever's heartbeat arrived
          // last, which would just alternate the two tabs forever.
          if (roleRef.current === 'writer' && shouldStepDown(tabId, [...peersRef.current.values()], desktopId, Date.now())) {
            setRoleAndAnnounce('follower')
          }
          break
        case 'bye': {
          peersRef.current.delete(m.tabId)
          // The writer left. Every remaining tab computes the successor from the
          // same rule and the same peer set, so exactly one promotes itself.
          if (roleRef.current === 'follower') {
            const self: PeerInfo = { tabId, desktopId, role: 'follower', lastSeen: Date.now() }
            const all = [...peersRef.current.values(), self]
            if (nextWriter(all, desktopId, Date.now()) === tabId) setRoleAndAnnounce('writer')
          }
          break
        }
        case 'state':
          if (m.desktopId === desktopId && roleRef.current === 'follower') setMirrored(m.state)
          break
      }
      setPeers(livePeersOn([...peersRef.current.values()], desktopId, tabId, Date.now()))
    }

    ch.postMessage({ kind: 'hello', tabId, desktopId, role: 'writer', at: Date.now() })

    // Decide after a short settle so replies to `hello` have arrived. Claiming
    // instantly would always elect this tab, since no peer could have answered
    // yet.
    const settle = setTimeout(() => {
      const decided = decideRole([...peersRef.current.values()], desktopId, Date.now())
      setRole(decided)
      ch.postMessage({ kind: 'heartbeat', tabId, desktopId, role: decided, at: Date.now() })
    }, 250)

    const beat = setInterval(() => {
      ch.postMessage({ kind: 'heartbeat', tabId, desktopId, role: roleRef.current, at: Date.now() })
      // Promote if the writer has gone quiet without saying goodbye — a crashed
      // or force-closed tab never sends 'bye', and without this the desktop
      // would be left with no writer and stop persisting entirely.
      if (roleRef.current === 'follower') {
        const now = Date.now()
        const others = [...peersRef.current.values()]
        if (decideRole(others, desktopId, now) === 'writer') {
          const self: PeerInfo = { tabId, desktopId, role: 'follower', lastSeen: now }
          if (nextWriter([...others, self], desktopId, now) === tabId) setRoleAndAnnounce('writer')
        }
      } else {
        // Belt-and-suspenders companion to the shouldStepDown check in
        // ch.onmessage: that one reacts the instant a rival writer's
        // heartbeat arrives, this one catches it on our own next tick even if
        // that message handler somehow didn't (e.g. messages coalesced, or a
        // rival's heartbeat and our own tick landed in an order that skipped
        // it). Same rule, same peer set shape, just a different trigger.
        if (shouldStepDown(tabId, [...peersRef.current.values()], desktopId, Date.now())) {
          setRoleAndAnnounce('follower')
        }
      }
      setPeers(livePeersOn([...peersRef.current.values()], desktopId, tabId, Date.now()))
    }, HEARTBEAT_MS)

    const bye = () => { try { ch.postMessage({ kind: 'bye', tabId, desktopId, at: Date.now() }) } catch { /* closing */ } }
    window.addEventListener('pagehide', bye)

    return () => {
      clearTimeout(settle)
      clearInterval(beat)
      window.removeEventListener('pagehide', bye)
      bye()
      ch.close()
      chanRef.current = null
    }
  }, [tabId, desktopId])

  return { tabId, role, peers, mirrored, publish }
}
