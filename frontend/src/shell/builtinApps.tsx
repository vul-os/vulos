/* eslint-disable react-refresh/only-export-components --
   This is a helper module (a builtin-id → lazy-component registry), not a
   component file. It intentionally exports the map + helpers alongside the lazy
   component consts; Fast Refresh doesn't apply. */
// builtinApps — the single source of truth mapping a builtin app id to its
// React component, shared by the Launchpad and the Home surface so both launch
// builtins identically (no divergent copies). Components are code-split via
// React.lazy and wrapped in a shared Suspense fallback.

import { createElement, lazy, Suspense, type ComponentType, type ReactElement } from 'react'
import Settings from '../core/Settings'
import { consumePendingSettingsSection } from '../core/settingsNav'
import { consumePendingLaunchQuery } from '../core/launchParams'

// core/Settings.jsx (untyped, out of scope) declares its default export as
// `function Settings({ initialSection } = {})` — an untyped destructured
// param with an object-literal default. TS's allowJs inference reads the
// `{}` default as the whole prop type, so the component's *inferred* type
// accepts no props at all, even though `initialSection` is read directly out
// of that destructure. This is the one place in this file the real prop the
// component reads isn't visible from its own (untyped) signature, so it's
// restated here rather than widening BUILTIN_COMPONENTS' factory type.
const SettingsTyped = Settings as ComponentType<{ initialSection?: string | null }>

const Terminal = lazy(() => import('../builtin/terminal/Terminal'))
const ActivityMonitor = lazy(() => import('../builtin/activity/ActivityMonitor'))
const FileManager = lazy(() => import('../builtin/files/FileManager'))
const Drive = lazy(() => import('../builtin/drive/Drive'))
const AppHub = lazy(() => import('../builtin/apphub/AppHub'))
const Drivers = lazy(() => import('../builtin/drivers/Drivers'))
const Packages = lazy(() => import('../builtin/packages/Packages'))
const DiskUsage = lazy(() => import('../builtin/disks/DiskUsage'))
const Authenticator = lazy(() => import('../apps/Authenticator/Authenticator'))
const Vault = lazy(() => import('../apps/Vault/Vault'))
const Messages = lazy(() => import('../builtin/peering/Messages'))
const DashboardApp = lazy(() => import('../builtin/dashboard/DashboardApp'))
const Assistant = lazy(() => import('../builtin/assistant/Assistant'))
const Calendar = lazy(() => import('../builtin/calendar/Calendar'))
const Contacts = lazy(() => import('../builtin/contacts/Contacts'))
const Phone = lazy(() => import('../builtin/phone/Phone'))

const loadingEl = () => createElement('div', { className: 'p-4 text-neutral-500' }, 'Loading...')
// Cmp is always a React.lazy-wrapped component from an untyped .jsx module;
// its real prop shape lives in that file, not here. Keeping wrap() generic
// over the component's own inferred prop type (rather than writing `any`)
// means each call site — including the one that threads `initialQuery` to
// Calendar below — is still checked against whatever TS could infer for
// that component, instead of opting the whole registry out of checking.
function wrap<P extends object>(Cmp: ComponentType<P>, props?: P): ReactElement {
  return createElement(Suspense, { fallback: loadingEl() }, createElement(Cmp, props))
}

/** A factory returning a fresh React element for a builtin app id. */
type BuiltinFactory = () => ReactElement

// BUILTIN_COMPONENTS maps app.id → a factory returning a fresh React element.
export const BUILTIN_COMPONENTS: Record<string, BuiltinFactory> = {
  persona: () => createElement(SettingsTyped, { initialSection: consumePendingSettingsSection() }),
  terminal: () => wrap(Terminal),
  activity: () => wrap(ActivityMonitor),
  files: () => wrap(FileManager),
  drive: () => wrap(Drive),
  apphub: () => wrap(AppHub),
  drivers: () => wrap(Drivers),
  packages: () => wrap(Packages),
  disks: () => wrap(DiskUsage),
  authenticator: () => wrap(Authenticator),
  vault: () => wrap(Vault),
  messages: () => wrap(Messages),
  dashboard: () => wrap(DashboardApp),
  assistant: () => wrap(Assistant),
  // PIM: standalone Calendar + Contacts over lilmail's /v1 (via /api/pim/*).
  // Calendar honours a deep-link query (e.g. ⌘K "New event" → ?action=new,
  // which pre-opens the new-event editor). consumePendingLaunchQuery clears it
  // so a later plain launch opens the plain month view.
  'vulos-calendar': () => wrap(Calendar, { initialQuery: consumePendingLaunchQuery('vulos-calendar') }),
  'vulos-contacts': () => wrap(Contacts),
  // Android Telephony bridge (SMS + calls) — only live inside the Vulos app on
  // a device with a GSM SIM; feature-detects nativeBridge.telephony itself.
  'vulos-phone': () => wrap(Phone),
}

// Apps that should only ever have one window open at a time.
export const BUILTIN_SINGLETONS: Set<string> = new Set(['persona', 'apphub', 'dashboard', 'assistant'])

// builtinComponent returns a fresh element for a builtin id, or null if the id
// is not a React builtin (e.g. a web/stream app).
export function builtinComponent(appId: string): ReactElement | null {
  const factory = BUILTIN_COMPONENTS[appId]
  return factory ? factory() : null
}

export function isBuiltinComponent(appId: string): boolean {
  return Object.prototype.hasOwnProperty.call(BUILTIN_COMPONENTS, appId)
}
