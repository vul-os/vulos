// useShellSession.ts — the React half of the cross-tab shell session.
//
// Announces this tab, tracks its peers, elects a single writer per desktop, and
// tells the shell whether it owns the persisted state or is mirroring someone
// else's. The rules themselves live in shellSession.ts as pure functions so
// they can be tested without a browser, a timer or a second tab.
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
  const tabId = useMemo(newTabId, [])
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
    if (!ch) {
      setRole('writer')
      return
    }

    const note = (m: { tabId: string; desktopId: string; role?: SessionRole }) => {
      peersRef.current.set(m.tabId, {
        tabId: m.tabId,
        desktopId: m.desktopId,
        role: m.role ?? 'follower',
        lastSeen: Date.now(),
      })
    }

    ch.onmessage = (ev) => {
      const m = ev.data as SessionMessage
      if (!m || m.tabId === tabId) return

      switch (m.kind) {
        case 'hello':
          note(m)
          // Answer a newcomer immediately rather than making it wait a full
          // heartbeat to discover us — otherwise a second tab opening next to a
          // live writer sees an empty peer set, decides it is the writer, and
          // both save until the next tick.
          ch.postMessage({ kind: 'heartbeat', tabId, desktopId, role: roleRef.current, at: Date.now() })
          break
        case 'heartbeat':
          note(m)
          break
        case 'bye': {
          peersRef.current.delete(m.tabId)
          // The writer left. Every remaining tab computes the successor from the
          // same rule and the same peer set, so exactly one promotes itself.
          if (roleRef.current === 'follower') {
            const self: PeerInfo = { tabId, desktopId, role: 'follower', lastSeen: Date.now() }
            const all = [...peersRef.current.values(), self]
            if (nextWriter(all, desktopId, Date.now()) === tabId) setRole('writer')
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
          if (nextWriter([...others, self], desktopId, now) === tabId) setRole('writer')
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
