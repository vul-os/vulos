// notificationTypes.ts — the shapes core/notificationStore.js's own header
// comment documents (untyped JS, out of scope for this pass), restated here so
// NotificationCenter.tsx and Toasts.tsx — the two shell surfaces that render
// the live store — share ONE definition instead of two drifting ones.
export interface NotificationAction {
  id: string
  label?: string
  run?: () => void
  route?: { app: string; query?: string }
  url?: string
}

export type NotificationLevel = 'info' | 'warning' | 'urgent' | 'critical'

export interface ShellNotification {
  id: string
  title: string
  body?: string
  source: string
  level: NotificationLevel
  actions?: NotificationAction[]
  // Toasts.jsx reads a singular `.action` (string URL) defensively alongside
  // `.actions[]`, but core/notificationStore.js's normalize() only ever
  // assembles the fields above — it never copies an `action` key through from
  // notify()'s input. Preserved (not dropped) so that read stays exactly as
  // inert as it always was, matching how ShellProvider documents its own
  // known-unused `singleton` field rather than silently deleting the check.
  action?: string
  ttl?: number
  read: boolean
  ts: number
  origin: 'local' | 'remote'
}

/** A live toast-queue entry (notificationStore's `toasts` array). */
export interface ToastEntry {
  key: string
  notif: ShellNotification
}

export interface NotificationPrefs {
  muted: boolean
  sound: boolean
  sources: Record<string, boolean>
}
