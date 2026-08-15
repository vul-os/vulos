// DesktopWidgets.tsx — the desktop's glanceable column.
//
// The desktop used to answer "what's going on" by covering itself with a
// full-bleed dashboard whenever no window was open, which is why it read as a
// web page rather than an OS. This is the replacement: a narrow, ambient
// column pinned to the right of the wallpaper that shows the same kind of
// state without owning the screen.
//
// WHAT CHANGED, AND WHY THIS FILE IS NOW SHORT
//
// The three widgets used to be three components declared right here, which meant
// "add a widget" was "edit the shell" and a user could add nothing at all. The
// column is now a HOST: it mounts src/widgets/host/WidgetRail, which reads a
// user-owned layout, resolves each entry against a registry, and renders it
// through a public API that a third party can build against (docs/WIDGETS.md).
// The clock, the agenda and the notification glance still exist — as widgets, in
// src/widgets/builtin/, registered through the same public entry point any other
// widget uses.
//
// Every widget is real state or an honest empty; none of them invent content.
// The column hides itself when Mission Control occupies the same layer (see
// DesktopCanvas), and on phones (MobileStack has its own home surface).
import { Z_DESKTOP_WIDGETS } from './zLayers'
// Side-effect import: registers every builtin widget with the registry. It must
// run before WidgetRail reads the registry, and a bare import at module scope is
// what guarantees that ordering.
import '../widgets/builtin'
import WidgetRail from '../widgets/host/WidgetRail'
import './shell-chrome.css'

export default function DesktopWidgets() {
  return (
    // Z_DESKTOP_WIDGETS puts the column ON THE DESKTOP, beneath every window.
    // This was written as a literal z-20 — the same value Window.tsx gives the
    // ACTIVE window — and because the column renders later in the DOM it won
    // the tie and floated OVER windows: the clock sat across Activity Monitor's
    // title bar and a notification card covered one of its metrics. Widgets are
    // wallpaper furniture, not an overlay; a window that reaches them should
    // cover them, exactly as on any desktop. The ordering now lives in
    // zLayers.ts and is asserted by zLayers.test.ts, which also checks that this
    // container carries no literal tailwind z-* class — so do not add one.
    //
    // The column is wider than it was (w-60 → w-72) because the rail is a
    // two-column grid now: a "small" widget is half the column, and at 240px
    // each half was 114px, which is narrower than any glanceable tile can be.
    <div
      data-desktop-widgets
      className="fixed right-3 w-72 max-w-[calc(100vw-1.5rem)] pointer-events-auto"
      style={{ top: '2.75rem', zIndex: Z_DESKTOP_WIDGETS }}
    >
      <WidgetRail />
    </div>
  )
}
