# The safe-area inset check that only a device can do

> **Who this is for.** The first person to hold an Android phone with the Vulos APK on it.
> **How long it takes.** Five minutes of setup once, then about a minute per run.
> **What it settles.** Two named defects in `clients/android/.../MainActivity.kt` that have
> never been compiled or run, and that no test in this repository can reach.
> **Status.** OPEN. This does not close by being written down. It closes by someone running it.

---

## 1. Why there is a gap here at all

`MainActivity.pushSafeAreaToShell()` writes the real window insets into the web shell by
setting `--safe-top` / `--safe-bottom` / `--safe-left` / `--safe-right` as **inline styles on
`<html>`**. That is the correct design — `env(safe-area-inset-*)` inside a WebView reports only
the display cutout, never the status bar or the navigation bar, so the native side genuinely
knows something CSS does not — but it means an inline style outranks the `:root { … env(…) }`
fallback in `frontend/src/index.css` at every specificity.

There is no Android SDK in this environment. That code is **parse-checked only**. Its author
named the two ways it can look right in review and fail on hardware:

| # | Risk | What it produces | Why review misses it |
|---|---|---|---|
| **A** | A wrong display-density divisor — physical pixels pushed where CSS pixels were meant | `"134.00px"` where `"34.00px"` was meant, at 390×844 | Syntactically perfect. Invisible on a 1× emulator; three times too large on a real 3× phone. |
| **B** | A lost `Locale.ROOT` in `String.format` | `"34,00px"` on any comma-decimal locale | `setProperty` accepts it, `padding-bottom` then resolves to **zero**, and every Chromium screenshot looks perfect because Chromium is never in that locale. |

The shell side of this is now defended and tested (`frontend/src/mobile/safeAreaInsets.ts`,
`frontend/e2e/insets-validation.e2e.ts`, 74 unit + 15 Chromium assertions). **That defence
does not make the Kotlin correct.** It makes a bad value non-destructive and, more usefully
for you, it makes a bad value *say what it is*. Everything below is a console read.

---

## 2. Setup — once, about five minutes

1. Build and install a debug APK: `cd clients/android && ./gradlew assembleDebug`, then
   `adb install -r app/build/outputs/apk/debug/app-debug.apk`.
   A `debug` build is inspectable; a **release** APK is not, because nothing in
   `MainActivity` calls `WebView.setWebContentsDebuggingEnabled(true)`. If you must inspect a
   release build, add that call first — and take it out again.
2. On the phone: Settings → About phone → tap *Build number* seven times → back → System →
   Developer options → **USB debugging** on.
3. Plug the phone into a laptop. In desktop Chrome open **`chrome://inspect/#devices`**, find
   the Vulos WebView, click *inspect*.
4. In that console, paste:

   ```js
   window.__vulosSafeArea
   ```

   That object is the whole instrument. It is published by the shell's inset guard on every
   pass, and it carries the raw string the native side actually sent.

**No laptop?** §6 has a purely visual version. It catches A and misses B.

---

## 3. Test 0 — did the bridge run at all? (do this first)

This is the one that can fake a pass for the other two. If `applyEdgeToEdgeInsets()` never
fires, every property is unset, nothing is malformed, nothing is oversized, and the diagnostics
look immaculate.

On the phone's **home screen**, in the WebView console:

```js
const d = window.__vulosSafeArea
;[d.pushes, d.checks['--safe-top'].reason, d.checks['--safe-top'].raw]
```

| Result | Meaning |
|---|---|
| `pushes` is `0`, or `--safe-top` reads `'empty'` | **FAIL — the bridge is dead.** Nothing reached the shell. The listener never fired, or `webView.evaluateJavascript` never ran. Everything below is meaningless until this is fixed. |
| `pushes` ≥ 1 and `--safe-top` reads `'ok'` with a non-zero `raw` | The channel works. Continue. |

A phone with a status bar always has a non-zero top inset. `--safe-top: "0.00px"` on a phone
is the same failure wearing a valid value.

---

## 4. Test A — the density divisor (one minute)

### The ten-second version

```js
window.__vulosSafeArea.flagged
```

| Result | Meaning |
|---|---|
| `[]` | Nothing exceeded 96 CSS px. **Not a pass on its own** — go to the arithmetic below. |
| `['--safe-bottom']`, or any edge listed | **FAIL.** No shipping device reports an inset that large. On a 3× phone this is what a missing `/ density` looks like. |

### The arithmetic version — needed on 2× devices

The 96px tripwire catches a 3× device comfortably. On a **2× device** a lost divisor can land
at exactly 96 and slip under it, so do this too:

```js
const d = window.__vulosSafeArea, r = devicePixelRatio
Object.fromEntries(Object.entries(d.checks).map(([k, v]) => [k, [v.px, v.px / r]]))
```

Compare the **first** number in each pair against what the device actually has:

| Screen chrome | Correct, in CSS px | 2× with the divisor lost | 3× with the divisor lost |
|---|---|---|---|
| Status bar (`--safe-top`) | 24 – 48 | 48 – 96 | 72 – 144 |
| Gesture-navigation pill (`--safe-bottom`) | 16 – 24 | 32 – 48 | 48 – 72 |
| 3-button navigation bar (`--safe-bottom`) | 48 | 96 | 144 |
| Display cutout, landscape (`--safe-left` / `--safe-right`) | 0 – 59 | 0 – 118 | 0 – 177 |

**PASS:** the first number is in the "correct" column.
**FAIL:** the first number is in a "divisor lost" column *and* the second number (`px / r`)
is the one in the correct column. That second number matching is the signature — it means the
value is in physical pixels.

Switch the phone between gesture navigation and 3-button navigation (Settings → System →
Gestures → Navigation mode) without leaving the app. `--safe-bottom` must change, live, from
about 24 to about 48. If it does not change at all, the listener is not re-firing.

### The fix, if it fails

`MainActivity.applyEdgeToEdgeInsets()`:

```kotlin
val d = resources.displayMetrics.density.takeIf { it > 0f } ?: 1f
pushSafeAreaToShell(top = bars.top / d, bottom = bars.bottom / d, …)
```

Every one of the four must be divided. A partial fix — three edges divided and one not — is
the version that survives a screenshot.

---

## 5. Test B — the lost `Locale.ROOT` (one minute)

**You do not need a phone from a comma-decimal country.** `String.format` without an explicit
locale uses `Locale.getDefault()`, which follows the *system* language.

1. Settings → System → Languages & input → Languages → **Add a language** →
   **Deutsch (Deutschland)** (or Français, Español, Português (Brasil), Nederlands, Türkçe,
   Afrikaans — all comma-decimal). Drag it to the top.
2. Force-stop Vulos and relaunch it. (A locale change recreates the Activity on its own, but
   force-stopping removes the doubt.)
3. In the WebView console:

   ```js
   const d = window.__vulosSafeArea
   ;[d.rejected, d.rejectedEver]
   ```

| Result | Meaning |
|---|---|
| `[]` and `{}` | **PASS.** `Locale.ROOT` is doing its job. Confirm by eye: `d.checks['--safe-bottom'].raw` contains a **dot** — `"34.00px"`. |
| Anything listed, with a `raw` containing a **comma** — `"34,00px"` | **FAIL.** The `Locale.ROOT` argument is missing or was dropped from one of the four `String.format` calls. |

`rejectedEver` is sticky and is never cleared, so a later benign inset event cannot erase the
evidence. `rejected` only reflects the most recent pass.

4. Put the phone's language back.

### The fix, if it fails

`MainActivity.pushSafeAreaToShell()` — the first argument to `String.format` must be
`java.util.Locale.ROOT`. One call site, four `%.2f`, all of them governed by that one argument.

### What this test does *not* do

The shell guard removes a comma-decimal value, which means the shell falls back to
`env(safe-area-inset-bottom)` — and inside a WebView that is **0**. So on a comma-decimal
locale the dock will still sit under the navigation bar until the Kotlin is fixed. What
changed is that it now does so *loudly*: a named property, the raw string, a reason, and a
console warning that says which of the two bugs you are looking at. The guard converts a
silent wrong number into a diagnosis. It is not a fix.

---

## 6. The no-laptop version

Open the app on the home screen and take **one screenshot**, in portrait, with 3-button
navigation turned on (the largest bottom inset, so the error is largest).

- The dock's row of icons must sit **immediately above** the navigation bar. Any part of a
  dock target underneath the navigation bar means the bottom inset is too small — which on a
  comma-decimal locale is test B failing, and otherwise means the bridge is dead (test 0).
- A visible empty band between the dock and the navigation bar — roughly a finger's width or
  more — means the bottom inset is too large: test A, the density divisor.
- The Vulos status bar's text must clear the system clock and battery icons without a large
  gap above it. Same two failure directions, top edge.

Rotate to landscape on a phone with a cutout and check that content clears the cutout on the
correct side.

This catches A. It cannot distinguish "test B failed" from "the bridge never ran", because
both produce a zero inset and an identical picture — which is exactly why §3 exists.

---

## 7. What is still not covered by any of this

- **A plausible-but-wrong inset that is not a factor-of-density wrong.** If the bridge reported
  40px where the device wanted 34px, nothing here would notice and nothing in the shell can.
  The 96px tripwire is a bound on absurdity, not a measurement.
- **`shell/home/Home.tsx`** reads `env(safe-area-inset-left/right)` **directly** rather than the
  `--safe-*` tokens, so the native push does not reach it at all. On Android it gets the display
  cutout only. That is a separate gap from the two above and is not fixed by fixing the Kotlin.
- **iOS / any other host.** There is none, and `env()` is correct there anyway.

---

## Related

- `roadmap/MOBILE-SHELL.md` §9 — what depends on these insets, and why an inline write makes a
  wrong value worse than an absent one
- `clients/android/DECISIONS.md` — MOB-12, edge-to-edge and the inset push
- `frontend/src/mobile/safeAreaInsets.ts` — the bounds, and why a too-large inset is flagged
  rather than rejected
- `frontend/e2e/insets-validation.e2e.ts` — the Chromium half, including the measurements that
  ruled `@property` and `max()` out as defences
