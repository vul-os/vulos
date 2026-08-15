// builtin/index.ts — the widgets this box ships with.
//
// Importing a widget module registers it: each one ends in
// `registerWidget(defineWidget(...))`, evaluated once at module load. This file
// is therefore the complete catalogue, and the ORDER here is the order the
// gallery lists them in.
//
// Nothing about a builtin is privileged. Each of these went through the same
// public `registerWidget` a third-party widget uses, holds the same manifest
// shape, is subject to the same default-deny permissions, and — for the sandboxed
// example — runs behind the same opaque-origin boundary. The rail cannot tell
// them apart, which is the point.

import './clock'
import './worldClock'
import './agenda'
import './pulse'
import './notifications'
import './notes'
import './stocks'

// A third-party widget, shipped so the sandboxed lane is exercised on every boot
// rather than only in a test. See examples/moon.ts.
import '../examples/moon'

export {}
