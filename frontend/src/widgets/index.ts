// index.ts — THE PUBLIC WIDGET API.
//
// This is the entire surface a widget author is expected to import from. If
// something is not re-exported here, a widget must not reach for it: everything
// else under src/widgets/ is host machinery, and importing it directly couples a
// widget to internals that will move.
//
// That rule is not advice — `src/widgets/__tests__/publicApi.test.ts` reads every
// builtin widget source and fails if any of them imports from anywhere but this
// module. The builtins are therefore held to the same import discipline a
// third-party widget is, which is the only way to know the public API is
// actually sufficient to build a widget with: if it weren't, a builtin would have
// had to cheat, and the test would say so.
//
// Developer documentation: src/widgets/WIDGETS.md (kept beside the API it
// documents, so the two are edited together)
// Design rationale + security model: roadmap/WIDGETS.md

export { defineWidget, defineSandboxedWidget, registerWidget } from './registry'

export { checkManifest } from './manifest'

export {
  WIDGET_SIZES, SIZE_SPAN, WIDGET_PERMISSIONS, PERMISSION_INFO, isSandboxed,
} from './types'

export type {
  WidgetSize,
  WidgetPermission,
  WidgetTick,
  WidgetManifest,
  WidgetSettingSpec,
  WidgetSettingValue,
  WidgetSettings,
  WidgetContext,
  WidgetStorage,
  WidgetNet,
  WidgetFetchResult,
  WidgetTelemetry,
  WidgetEvent,
  WidgetCalendar,
  WidgetNotification,
  WidgetNotifications,
  WidgetDefinition,
  SandboxedWidgetDefinition,
  AnyWidgetDefinition,
} from './types'

// Time-zone helpers. Offered as part of the API rather than left to each author
// because the naive version of this is wrong in four separate ways (see
// lib/tz.ts) and every clock-shaped widget would get it wrong identically.
export * as time from './lib/tz'

// Presentational primitives. A widget is free to render whatever it likes, but
// these carry the OS's type scale, spacing and token colours, so a widget built
// from them looks native without its author having to reverse-engineer the
// design system — and, more usefully, without hardcoding a hex that would fail
// the contrast gate in one of the two themes.
export {
  WidgetFrame,
  WidgetTitle,
  WidgetBigValue,
  WidgetLabel,
  WidgetEmpty,
  WidgetError,
  WidgetRowButton,
} from './host/primitives'
