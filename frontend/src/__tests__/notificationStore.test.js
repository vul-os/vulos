import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  notify, ingest, markRead, markAllRead, dismiss, clear,
  getItems, getUnreadCount, getToasts, dismissToast,
  getPrefs, setMuted, setSound, setSourceEnabled, isSourceEnabled,
  subscribe, subscribePrefs, setRemoteSink,
  normalize, shouldLog, shouldToast, mergeById, coercePrefs,
  __resetForTests,
} from '../core/notificationStore'
import {
  extractSignals, processHomeData, __resetForTests as resetNotifier,
} from '../core/notifiers/attentionNotifier'

// The notificationStore is the Wave-13 "notify()" seam: any surface posts to it,
// the shell bell + toaster subscribe. These pin the seam contract — dispatch,
// unread accounting, read/dismiss/clear, the pure filter logic (mute / per-source
// / critical bypass), rate-limiting, and the backend-bridge sink.

beforeEach(() => { __resetForTests(); resetNotifier() })

describe('notify() — posting', () => {
  it('adds a notification, increments unread, returns an id + dismiss handle', () => {
    const { id, dismiss: off } = notify({ title: 'Hello', source: 'mail' })
    expect(id).toBeTruthy()
    expect(getItems()).toHaveLength(1)
    expect(getItems()[0].title).toBe('Hello')
    expect(getUnreadCount()).toBe(1)
    off()
    expect(getItems()).toHaveLength(0)
  })

  it('ignores input without a title', () => {
    const { id } = notify({ source: 'mail' })
    expect(id).toBeNull()
    expect(getItems()).toHaveLength(0)
  })

  it('notifies item subscribers on post', () => {
    const spy = vi.fn()
    const unsub = subscribe(spy)
    notify({ title: 'x' })
    expect(spy).toHaveBeenCalled()
    unsub()
  })

  it('raises a transient toast for a normal notification', () => {
    notify({ title: 'toasty', source: 'mail' })
    expect(getToasts()).toHaveLength(1)
    expect(getToasts()[0].notif.title).toBe('toasty')
  })
})

describe('normalize()', () => {
  it('defaults level to info and coerces object bodies', () => {
    const n = normalize({ title: 'a', body: { detail: 'deep' } })
    expect(n.level).toBe('info')
    expect(n.body).toBe('deep')
    expect(n.origin).toBe('local')
    expect(n.read).toBe(false)
  })
  it('maps app → source and clamps unknown levels to info', () => {
    const n = normalize({ title: 'a', app: 'drive', level: 'bogus' })
    expect(n.source).toBe('drive')
    expect(n.level).toBe('info')
  })
  it('gives urgent/critical a sticky (0) default ttl', () => {
    expect(normalize({ title: 'a', level: 'urgent' }).ttl).toBe(0)
    expect(normalize({ title: 'a', level: 'info' }).ttl).toBeGreaterThan(0)
  })
})

describe('filter logic (pure)', () => {
  it('shouldLog is false only when a source is explicitly off', () => {
    expect(shouldLog({ sources: {} }, 'mail')).toBe(true)
    expect(shouldLog({ sources: { mail: false } }, 'mail')).toBe(false)
  })
  it('shouldToast honours mute but critical always breaks through', () => {
    expect(shouldToast({ muted: true, sources: {} }, 'mail', 'info')).toBe(false)
    expect(shouldToast({ muted: true, sources: {} }, 'mail', 'critical')).toBe(true)
    expect(shouldToast({ muted: false, sources: {} }, 'mail', 'info')).toBe(true)
  })
  it('a source that is off suppresses even critical toasts', () => {
    expect(shouldToast({ muted: false, sources: { mail: false } }, 'mail', 'critical')).toBe(false)
  })
})

describe('mergeById()', () => {
  it('inserts new, updates existing by id, keeps newest-first + bounded', () => {
    let list = []
    list = mergeById(list, { id: 'a', ts: 1 })
    list = mergeById(list, { id: 'b', ts: 2 })
    expect(list.map(n => n.id)).toEqual(['b', 'a'])
    list = mergeById(list, { id: 'a', ts: 3, read: true })
    expect(list).toHaveLength(2)
    expect(list.find(n => n.id === 'a').read).toBe(true)
    const big = Array.from({ length: 200 }, (_, i) => ({ id: `x${i}`, ts: 100 + i }))
    const merged = big.reduce((acc, n) => mergeById(acc, n, 100), [])
    expect(merged).toHaveLength(100)
  })
})

describe('mutations', () => {
  it('markRead flips a single item; markAllRead clears the count', () => {
    const a = notify({ title: 'a' }).id
    notify({ title: 'b' })
    expect(getUnreadCount()).toBe(2)
    markRead(a)
    expect(getUnreadCount()).toBe(1)
    markAllRead()
    expect(getUnreadCount()).toBe(0)
  })
  it('dismiss removes the item and its live toast', () => {
    const id = notify({ title: 'a' }).id
    expect(getToasts()).toHaveLength(1)
    dismiss(id)
    expect(getItems()).toHaveLength(0)
    expect(getToasts()).toHaveLength(0)
  })
  it('clear empties items and toasts', () => {
    notify({ title: 'a' }); notify({ title: 'b', source: 'x', ts: Date.now() + 9999 })
    clear()
    expect(getItems()).toHaveLength(0)
    expect(getToasts()).toHaveLength(0)
  })
  it('dismissToast removes a toast but keeps the logged item', () => {
    notify({ title: 'a' })
    const key = getToasts()[0].key
    dismissToast(key)
    expect(getToasts()).toHaveLength(0)
    expect(getItems()).toHaveLength(1)
  })
})

describe('prefs — mute, source, sound', () => {
  it('mute suppresses the toast but still logs the item (DND)', () => {
    setMuted(true)
    notify({ title: 'quiet', source: 'mail' })
    expect(getItems()).toHaveLength(1)
    expect(getToasts()).toHaveLength(0)
  })
  it('a critical breaks through mute', () => {
    setMuted(true)
    notify({ title: 'boom', source: 'system', level: 'critical' })
    expect(getToasts()).toHaveLength(1)
  })
  it('a source switched off drops the notification entirely', () => {
    setSourceEnabled('mail', false)
    expect(isSourceEnabled('mail')).toBe(false)
    notify({ title: 'dropped', source: 'mail' })
    expect(getItems()).toHaveLength(0)
    setSourceEnabled('mail', true)
    expect(isSourceEnabled('mail')).toBe(true)
    notify({ title: 'kept', source: 'mail' })
    expect(getItems()).toHaveLength(1)
  })
  it('setSound + subscribers fire on prefs change', () => {
    const spy = vi.fn()
    const unsub = subscribePrefs(spy)
    setSound(false)
    expect(getPrefs().sound).toBe(false)
    expect(spy).toHaveBeenCalled()
    unsub()
  })
  it('coercePrefs sanitises arbitrary input', () => {
    expect(coercePrefs(null)).toEqual({ muted: false, sound: true, sources: {} })
    expect(coercePrefs({ muted: 1, sound: false, sources: { a: false } }))
      .toEqual({ muted: true, sound: false, sources: { a: false } })
  })
})

describe('rate limiting (quiet)', () => {
  it('collapses a rapid burst from one source into a single toast', () => {
    const base = Date.now()
    notify({ title: '1', source: 'mail', ts: base })
    notify({ title: '2', source: 'mail', ts: base + 100 })
    notify({ title: '3', source: 'mail', ts: base + 200 })
    // All logged...
    expect(getItems()).toHaveLength(3)
    // ...but only the first within the rate window toasts.
    expect(getToasts()).toHaveLength(1)
  })
  it('lets a different source through the window', () => {
    const base = Date.now()
    notify({ title: '1', source: 'mail', ts: base })
    notify({ title: '2', source: 'assistant', ts: base + 100 })
    expect(getToasts()).toHaveLength(2)
  })
})

describe('backend bridge (ingest + remote sink)', () => {
  it('ingest marks origin remote and proxies mark-read to the sink', () => {
    const sink = { markRead: vi.fn(), markAllRead: vi.fn(), clear: vi.fn() }
    setRemoteSink(sink)
    ingest({ id: 'srv-1', title: 'server', source: 'system' })
    const item = getItems()[0]
    expect(item.origin).toBe('remote')
    markRead('srv-1')
    expect(sink.markRead).toHaveBeenCalledWith('srv-1')
  })
  it('local items do NOT hit the remote sink', () => {
    const sink = { markRead: vi.fn() }
    setRemoteSink(sink)
    const id = notify({ title: 'local' }).id
    markRead(id)
    expect(sink.markRead).not.toHaveBeenCalled()
  })
})

describe('attentionNotifier — real producer', () => {
  it('extractSignals pulls focus + unread mail', () => {
    const { focus, unreadMail } = extractSignals({
      focus: [{ uid: 'f1' }],
      activity: [{ uid: 'm1', unread: true }, { uid: 'm2', unread: false }],
    })
    expect(focus).toHaveLength(1)
    expect(unreadMail).toHaveLength(1)
    expect(unreadMail[0].uid).toBe('m1')
  })

  it('first poll SEEDS the baseline silently, then only new items notify', () => {
    // First poll: existing backlog — must not spam.
    const seeded = processHomeData({ focus: [{ uid: 'f1', subject: 'old' }], activity: [] })
    expect(seeded).toBe(0)
    expect(getItems()).toHaveLength(0)
    // Second poll: a genuinely new focus item fires exactly one notification.
    const emitted = processHomeData({
      focus: [{ uid: 'f1', subject: 'old' }, { uid: 'f2', subject: 'new', from: 'Ada' }],
      activity: [],
    })
    expect(emitted).toBe(1)
    expect(getItems()).toHaveLength(1)
    expect(getItems()[0].source).toBe('assistant')
    // Third poll with nothing new: no duplicates.
    expect(processHomeData({ focus: [{ uid: 'f2' }], activity: [] })).toBe(0)
    expect(getItems()).toHaveLength(1)
  })
})
