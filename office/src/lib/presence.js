/**
 * presence.js — Vulos Office Presence layer (OFFICE-24).
 *
 * Broadcasts {accountId, displayName, color, online} over a dedicated
 * "presence" channel on the OFFICE-20 FabricClient (separate from CRDT ops).
 *
 * Usage:
 *   const pm = new PresenceManager({ fabric, localIdentity })
 *   pm.addEventListener('roster', ({ detail: peers }) => …)
 *   pm.join()
 *   pm.leave()
 *
 * Identity resolution order:
 *   1. opts.localIdentity (caller-supplied, from Vulos account/vumail)
 *   2. localStorage "presence_identity" (persisted guest identity)
 *   3. Generated guest identity (random name + color, persisted)
 */

const PRESENCE_CHANNEL = 'presence'
const HEARTBEAT_MS = 10_000        // send heartbeat every 10 s
const TIMEOUT_MS = 25_000          // drop peer after 25 s of silence

// Valid status values for OFFICE-62
export const STATUS_ONLINE = 'online'
export const STATUS_AWAY   = 'away'
export const STATUS_DND    = 'dnd'
export const STATUS_IN_CALL = 'in-a-call'  // set by OFFICE-63 calling layer

/** Deterministic color from a string (stable across sessions). */
function colorFromString(str) {
  let hash = 0
  for (let i = 0; i < str.length; i++) {
    hash = (hash << 5) - hash + str.charCodeAt(i)
    hash |= 0
  }
  const hue = Math.abs(hash) % 360
  return `hsl(${hue}, 65%, 50%)`
}

const GUEST_ADJECTIVES = ['Swift', 'Bright', 'Calm', 'Bold', 'Kind']
const GUEST_ANIMALS = ['Lemur', 'Falcon', 'Otter', 'Fox', 'Lynx']

function randomGuestName() {
  const adj = GUEST_ADJECTIVES[Math.floor(Math.random() * GUEST_ADJECTIVES.length)]
  const ani = GUEST_ANIMALS[Math.floor(Math.random() * GUEST_ANIMALS.length)]
  return `${adj} ${ani}`
}

function loadOrCreateLocalIdentity() {
  try {
    const stored = localStorage.getItem('presence_identity')
    if (stored) {
      const parsed = JSON.parse(stored)
      if (parsed.accountId && parsed.displayName) return parsed
    }
  } catch { /* ignore */ }
  const identity = {
    accountId: `guest:${crypto.randomUUID()}`,
    displayName: randomGuestName(),
    isGuest: true,
  }
  try { localStorage.setItem('presence_identity', JSON.stringify(identity)) } catch { /* ignore */ }
  return identity
}

export class PresenceManager extends EventTarget {
  /**
   * @param {object} opts
   * @param {import('./fabric.js').FabricClient} opts.fabric
   * @param {{ accountId?: string, displayName?: string, isGuest?: boolean }} [opts.localIdentity]
   *   Pass the Vulos account identity if authenticated; omit for guest.
   */
  constructor({ fabric, localIdentity = null }) {
    super()
    this._fabric = fabric

    const baseIdentity = localIdentity || loadOrCreateLocalIdentity()
    this._local = {
      accountId: baseIdentity.accountId,
      displayName: baseIdentity.displayName,
      color: colorFromString(baseIdentity.accountId),
      online: true,
      status: STATUS_ONLINE,    // OFFICE-62: online | away | dnd | in-a-call
      statusText: '',           // OFFICE-62: free-text custom status
      isGuest: baseIdentity.isGuest ?? false,
      ts: Date.now(),
    }

    /** @type {Map<string, { accountId, displayName, color, online, ts }>} */
    this._roster = new Map()
    this._heartbeatTimer = null
    this._gcTimer = null
    this._stopped = false

    // Listen for presence frames on the fabric message channel.
    this._onFabricMessage = this._handleMessage.bind(this)
    this._fabric.addEventListener('message', this._onFabricMessage)

    // Also re-broadcast on new peer connections so late joiners see us immediately.
    this._onFabricState = this._handleState.bind(this)
    this._fabric.addEventListener('state', this._onFabricState)
  }

  // ─── Public API ─────────────────────────────────────────────────────────────

  /** Start presence: broadcast join + begin heartbeat. */
  join() {
    this._broadcast()
    this._heartbeatTimer = setInterval(() => this._broadcast(), HEARTBEAT_MS)
    this._gcTimer = setInterval(() => this._gc(), HEARTBEAT_MS)
  }

  /**
   * OFFICE-62: Update local status and broadcast immediately.
   * @param {string} status  - one of STATUS_ONLINE | STATUS_AWAY | STATUS_DND | STATUS_IN_CALL
   * @param {string} [text]  - optional free-text custom status
   */
  setStatus(status, text = '') {
    this._local.status = status || STATUS_ONLINE
    this._local.statusText = text || ''
    this._broadcast()
  }

  /** Stop presence: broadcast leave, clear timers. */
  leave() {
    this._stopped = true
    clearInterval(this._heartbeatTimer)
    clearInterval(this._gcTimer)
    this._broadcastLeave()
    this._fabric.removeEventListener('message', this._onFabricMessage)
    this._fabric.removeEventListener('state', this._onFabricState)
  }

  /** Current roster snapshot (excludes self). Array of peer identity objects. */
  get roster() {
    return [...this._roster.values()]
  }

  /** Full roster including the local user. */
  get fullRoster() {
    return [{ ...this._local, isSelf: true }, ...this.roster]
  }

  // ─── Internal ───────────────────────────────────────────────────────────────

  _broadcast() {
    if (this._stopped) return
    this._local.ts = Date.now()
    this._sendPresenceFrame({ ...this._local, type: 'join' })
  }

  _broadcastLeave() {
    this._sendPresenceFrame({ ...this._local, type: 'leave' })
  }

  _sendPresenceFrame(payload) {
    const frame = JSON.stringify({ channel: PRESENCE_CHANNEL, payload })
    this._fabric.send(frame)
  }

  _handleMessage({ detail: { from, data } }) {
    let text
    try {
      text = typeof data === 'string' ? data : new TextDecoder().decode(data)
    } catch { return }
    let frame
    try { frame = JSON.parse(text) } catch { return }
    if (frame.channel !== PRESENCE_CHANNEL) return
    const p = frame.payload
    if (!p || !p.accountId || p.accountId === this._local.accountId) return

    if (p.type === 'leave') {
      this._roster.delete(p.accountId)
    } else {
      this._roster.set(p.accountId, {
        accountId: p.accountId,
        displayName: p.displayName || 'Unknown',
        color: p.color || colorFromString(p.accountId),
        online: true,
        status: p.status || STATUS_ONLINE,          // OFFICE-62
        statusText: p.statusText || '',              // OFFICE-62
        isGuest: p.isGuest ?? false,
        ts: Date.now(),
        peerId: from,
      })
    }
    this._emitRoster()
  }

  _handleState({ detail: { state } }) {
    // Re-announce ourselves whenever a new peer connects.
    if (state === 'connected' || state === 'relay') {
      this._broadcast()
    }
  }

  /** Remove peers that haven't sent a heartbeat within TIMEOUT_MS. */
  _gc() {
    const now = Date.now()
    let changed = false
    for (const [id, peer] of this._roster) {
      if (now - peer.ts > TIMEOUT_MS) {
        this._roster.delete(id)
        changed = true
      }
    }
    if (changed) this._emitRoster()
  }

  _emitRoster() {
    this.dispatchEvent(new CustomEvent('roster', { detail: this.fullRoster }))
  }
}

// ─── React hook ─────────────────────────────────────────────────────────────

import { useEffect, useRef, useState } from 'react'

/**
 * usePresence — React hook that manages a PresenceManager lifecycle.
 *
 * @param {object} opts
 * @param {import('./fabric.js').FabricClient | null} opts.fabric
 * @param {{ accountId?: string, displayName?: string } | null} [opts.localIdentity]
 * @returns {{ roster: Array, manager: PresenceManager | null }}
 *
 * Returns the full roster (including self with isSelf=true) while the fabric
 * is live; returns [] when fabric is null (editor opened without collab).
 * OFFICE-62: also returns manager so callers can call manager.setStatus(status, text).
 */
export function usePresence({ fabric, localIdentity = null }) {
  const [roster, setRoster] = useState([])
  const pmRef = useRef(null)

  useEffect(() => {
    if (!fabric) {
      setRoster([])
      return
    }

    const pm = new PresenceManager({ fabric, localIdentity })
    pmRef.current = pm

    const onRoster = ({ detail }) => setRoster(detail)
    pm.addEventListener('roster', onRoster)
    pm.join()

    return () => {
      pm.removeEventListener('roster', onRoster)
      pm.leave()
      pmRef.current = null
    }
  }, [fabric]) // eslint-disable-line react-hooks/exhaustive-deps

  return { roster, manager: pmRef.current }
}
