import { useSyncExternalStore } from 'react'
import { activeFormFactor, getDockProfile, getLayout, getLayoutVersion, subscribeLayout } from './store'
import type { DesktopLayout, DockProfile, FormFactor } from './types'

/**
 * Read the live layout.
 *
 * useSyncExternalStore over a module store, so no provider has to be mounted
 * anywhere (App.tsx's provider tree is owned elsewhere). The snapshot is a
 * monotonic VERSION NUMBER rather than the layout object: React compares
 * snapshots with Object.is, and returning the mutable-on-commit `current`
 * object would either tear or loop depending on whether it was replaced.
 */
export function useDesktopLayout(): DesktopLayout {
  useSyncExternalStore(subscribeLayout, getLayoutVersion, getLayoutVersion)
  return getLayout()
}

/** The dock profile in effect, or a named form factor's. */
export function useDockProfile(formFactor?: FormFactor): DockProfile {
  useSyncExternalStore(subscribeLayout, getLayoutVersion, getLayoutVersion)
  return getDockProfile(formFactor ?? activeFormFactor())
}
