// appTypes.ts — extends core/AppRegistry.ts's real `App` type with the handful
// of extra fields shell-local code either attaches to an app-like object that
// never goes through the registry (Launchpad's desktop-entries merge, below)
// or reads defensively without ever producing (launchApp's `lane_web` check —
// see the comment on that field). Reusing `App` here (rather than a second,
// independent shape) is the point: AppRegistry.ts is already the typed source
// of truth for a *real* registry entry, so this only adds what it doesn't
// cover instead of redeclaring it.
import type { App } from '../core/AppRegistry'

export interface AppEntry extends App {
  // ROUTER-02 lane hint launchApp.ts checks — no registry entry (builtin or
  // installed) has ever set this; the check is preserved as inert, exactly as
  // it was in the original .js, not removed as part of this typing pass.
  lane_web?: boolean
  fps?: number
  _fps?: number
  // Desktop-entry (apt-installed GUI app) fields — Launchpad.tsx maps
  // GET /api/desktop/entries results onto an app-shaped object carrying
  // these, merged alongside real registry Apps for the tile grid and launch
  // dispatch. See Launchpad.tsx's desktopApps mapping.
  _desktop?: boolean
  _exec?: string
}
