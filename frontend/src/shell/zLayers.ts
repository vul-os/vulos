// zLayers.ts — the desktop's stacking order, in one place.
//
// These used to be magic numbers scattered across the shell, and they drifted:
// the ambient widget column was written as `z-20`, the same value Window.tsx
// gives the ACTIVE window. Because the column renders later in the DOM it won
// the tie and floated over windows — the clock sat across Activity Monitor's
// title bar and a notification card covered one of its metrics. Nothing failed;
// it just looked broken, and only a screenshot showed it.
//
// Keeping the layers here means the ordering is one readable list, and
// zLayers.test.ts asserts the relationships that actually matter rather than
// trusting that two numbers written in different files still agree.

/** Ambient desktop furniture (the widget column). Below every window: a
 *  window that reaches it should cover it, as on any desktop. */
export const Z_DESKTOP_WIDGETS = 5

/** A window that is not focused. */
export const WINDOW_Z_INACTIVE = 10

/** The focused window. */
export const WINDOW_Z_ACTIVE = 20

/** A window mid-close, lifted briefly so its exit animation is not occluded
 *  by the window revealed beneath it. */
export const WINDOW_Z_CLOSING_LIFT = 5
