# Location (LOCATION-01) — browser → box position feed

## Goal

The box has no GPS. Location has to come from whatever client is currently
looking at it — a phone's PWA (which *does* have a real GPS chip), a laptop
browser (WiFi/IP geolocation only — coarse), or, on hardware that has it, the
box's own GSM modem. This document is the architecture for getting a position
from "whatever client is attached" into "whatever app on the box wants it,"
across three tiers of consumer, honestly split by what's actually built versus
designed.

## Non-goals

- Not a maps/routing engine — see [`DEFAULT-WEB-APPS.md`](DEFAULT-WEB-APPS.md)
  for `apps/maps` (Leaflet-based, already calls `navigator.geolocation` directly
  in-browser for "locate me" — that local, single-app use is unaffected by
  anything here).
- Not a tracking/history product — this is a live-position feed with staleness
  surfaced, not a location-history store, unless a future app explicitly opts
  into logging what it receives.
- Not phone-as-instance — consistent with [`MOB-01`](../clients/android/DECISIONS.md#mob-01--the-phone-is-a-thin-client-not-an-instance),
  the phone contributes a *reading*, it doesn't become part of the box's
  authority.

## The core idea

```
┌──────────────┐   position    ┌─────────────────┐   position   ┌──────────────────┐
│ Phone PWA     │ ────────────▶ │                 │ ───────────▶ │ Vulos-native apps │
│ (real GPS)    │  (best src)   │  Box: /api/     │  (tier ①)    │ via /api/location │
└──────────────┘               │  location       │               └──────────────────┘
┌──────────────┐   position    │  service         │   feed       ┌──────────────────┐
│ Laptop browser│ ────────────▶ │  (per-user      │ ───────────▶ │ Native Linux apps │
│ (WiFi-geo,    │  (fallback,   │  scoped)        │  (tier ②)    │ via virtual gpsd  │
│  coarse)      │   coarse)     │                 │               │ → GeoClue         │
└──────────────┘               │                 │               └──────────────────┘
                                │                 │   feed       ┌──────────────────┐
        ModemManager           │                 │ ───────────▶ │ Streamed Android  │
        --location-get         │                 │  (tier ③)    │ (redroid) via     │
        (real box-side GNSS,   │                 │               │ virtual GNSS/NMEA │
         if the modem has it) ─┼────────────────▶│                └──────────────────┘
                                └─────────────────┘
```

- **The box has no GPS of its own.** Position has to be *reported to* the box
  by a client, then redistributed. This mirrors how telephony works the other
  way (a SIM physically in the box, per [`backend/services/telephony`](../backend/services/telephony))
  — location is the mirror image: the sensor is in the *client*, not the box.
- **Phone PWA is the good source.** A phone's browser `navigator.geolocation`
  is backed by the real GNSS chip — accurate to a few meters, refreshes
  quickly, has heading/speed. This is the primary source whenever a phone with
  the Vulos PWA installed (see [`clients/android/README.md`](../clients/android/README.md),
  [`MOB-02`](../clients/android/DECISIONS.md#mob-02--two-delivery-tiers-one-shell-pwa-first))
  is attached and has granted location permission.
- **Laptop WiFi-geo is the coarse fallback.** A desktop browser's
  `navigator.geolocation` on a machine with no GPS resolves via WiFi
  BSSID/IP-geolocation lookups (browser-vendor services, e.g. Google's/Mozilla's
  location APIs) — city-or-neighborhood accuracy, not street-level. Used only
  when no better source is attached; the age/tier of the reading is always
  surfaced (see **Permission and honesty** below) so an app can decide whether
  "somewhere in the neighborhood, 40 minutes old" is good enough for its purpose.
- **ModemManager fallback.** If the box has a USB LTE modem/SIM already
  serving [`telephony`](../backend/services/telephony) *and* that modem
  exposes GNSS, `mmcli --location-get` (or the modem-manager D-Bus `Location`
  interface) returns a real box-side fix — no client needed at all. This is
  the one path where the box itself is the source, not a relayed client
  reading. Same `mmcli` shell-out style as telephony (see
  [`telephony.go`](../backend/services/telephony/telephony.go)'s doc comment
  on why: stable CLI key-value output across ModemManager versions beats a Go
  D-Bus binding).

## Per-user scoping

Location is **per-client**, the same posture as
[`backend/services/telephony`](../backend/services/telephony)'s owner-scoping,
but one notch more granular: telephony scopes to the box *owner* (one SIM, one
line, one owner); location scopes to **whichever authenticated user's client
sent the reading** — a multi-user box has each user's phone/laptop reporting
its *own* position, keyed by `X-User-ID` (stamped by the auth middleware, per
repo convention), not funneled through a single owner concept. Two users on
the same box do not see each other's position; each app request for "my
location" resolves against the requesting user's own most-recent reading only.

## Permission model

- **Opt-in.** No position is read or reported until the user explicitly grants
  it — both the browser-level `navigator.geolocation` permission prompt *and*
  a Vulos-side "let this box use my location" toggle. Consistent with
  [`OFFLINE-AUTH.md`](OFFLINE-AUTH.md) and the mobile security floor's
  "opt-in, revocable" posture for anything sensor-adjacent (see
  [`HANDOVER.md`](../clients/android/HANDOVER.md)'s security-floor section).
- **Revocable at any time** — turning it off stops new reports; it does not
  retroactively erase what's already been read by an app (apps that need
  history are responsible for their own retention/deletion, same as any other
  per-app data).
- **Staleness and source tier are always surfaced**, never silently
  interpolated or hidden. Every reading exposed to a consumer carries at
  minimum: `lat`, `lon`, `accuracyMeters`, `source` (`phone-gps` /
  `browser-wifi` / `modem-gnss`), and `ageSeconds` since it was captured. An
  app asking "where is the user" gets the honest shape of the answer, not a
  synthetic always-fresh point.
- **Never claim precision the source doesn't have.** A WiFi-geo fallback
  reading must not be presented to a consumer as GPS-grade; `accuracyMeters`
  is the mechanism, not a marketing gloss.

## The three consumer tiers

### ① Vulos-native apps via `/api/location` — status: **built**

Vulos apps (Maps beyond its own in-browser "locate me," Weather, any future
app that wants "where is the user right now") read the redistributed position
straight from the box's own `/api/location` service over the normal
authenticated API surface (`X-User-ID` scoped, as above). This is the native
path — no OS-level plumbing needed, because both the reporter (an app's own
page/PWA calling `navigator.geolocation` and POSTing it up) and the consumer
are just web apps talking to the box's API, the same shape as every other
first-party service. The **PWA reporter** (the piece on the phone side that
watches position and pushes updates while granted) is built alongside it.

### ② Native Linux apps via a virtual gpsd → GeoClue feed — status: **design / best-effort, pending hardware**

Linux desktop apps don't talk to a Vulos-specific HTTP API for location — they
talk to **GeoClue** (the standard Linux desktop geolocation D-Bus service),
which in turn is typically backed by **gpsd** for apps or setups that expect a
real GPS device. The plan: run a small bridge process that takes the box's
current best position (from tier-①'s pipeline) and serves it as if it were a
locally-attached GPS — either by:
- feeding gpsd's protocol directly (gpsd supports non-hardware/"fake" GPS
  sources for exactly this kind of virtualization), so any gpsd-aware app sees
  a normal-looking fix, or
- implementing the minimal GeoClue backend interface directly and skipping
  gpsd if a given distro's GeoClue is configured to prefer non-gpsd sources.

This is **not yet built** — it needs a real Linux desktop session with GeoClue
present to verify against (which app would even ask GeoClue for location, and
does the box's compositor session route D-Bus session-bus calls the way a
normal desktop does). Marked best-effort/pending hardware-and-session
verification per this repo's honesty rule: no claim of "works" without having
run it against a real GeoClue consumer.

### ③ Streamed Android (redroid) via a virtual GNSS/NMEA feed — status: **design / best-effort, pending hardware**

Android's location stack ultimately wants a **GNSS HAL** or, for many
apps/tools, will accept an injected **NMEA** sentence stream as if it came
from a real GPS receiver. For a redroid container (see
[`clients/android/ANDROID-COMPAT.md`](../clients/android/ANDROID-COMPAT.md) — opt-in Android
streaming) to give its guest apps a working "where am I," the plan is to feed
it the box's current best position tier-①-style, translated into NMEA
sentences (or the mock-location-provider equivalent Android exposes for test
GPS, but see the critical distinction below) at a redroid-visible location
source.

**Critical distinction from Android's "mock location" API:** Android exposes a
developer-facing *mock location provider* API specifically so a value can be
force-set from software — and every serious app (especially ride-hailing, per
[`clients/android/ANDROID-COMPAT.md`](../clients/android/ANDROID-COMPAT.md)'s attestation-locked
bucket) treats `isMock == true` as an active fraud/abuse signal and refuses or
flags it. **This tier is explicitly designed to set `isMock == false`** — i.e.
present as a genuine hardware-backed GNSS/NMEA source at the driver level, the
same way a gpsd-fed device would, *not* through the mock-location developer
API. This matters for legitimate uses in bucket-③ apps (regional apps, IoT
companion apps — see the compat doc) that have no argument against reading a
real-looking position. It does **not** change the verdict on ride-hailing
apps: they cross-check GPS plausibility against network/cell signals and
account/device history well beyond the mock flag, and a redroid container is
independently likely to fail their Play-Integrity check regardless of how
"real" the location feed looks. **This tier does not exist to make ride-hailing
work** — see the compat doc's explicit warning; it exists for the honest
③-bucket apps that have no other blocker.

This is **not yet built**. It needs a real redroid container running on real
box hardware to verify the GNSS/NMEA injection point actually works against a
guest app (which injection surface a given redroid build accepts, whether the
guest kernel module even exposes one) — marked best-effort/pending hardware
per this repo's rule against claiming unverified hardware-dependent behavior
works.

## Status summary

| Tier | Consumer | Status |
|---|---|---|
| — | `/api/location` service + PWA reporter | **built** |
| ① | Vulos-native apps | **built** — reads `/api/location` directly |
| ② | Native Linux apps | design — virtual gpsd → GeoClue feed, pending real-GeoClue-session verification |
| ③ | Streamed Android (redroid) | design — virtual GNSS/NMEA feed, `isMock=false`, pending real-redroid-hardware verification |
| — | ModemManager `--location-get` | design — real box-side source when the attached GSM modem has GNSS; same `mmcli` pattern as telephony |

## Related

- [`clients/android/ANDROID-COMPAT.md`](../clients/android/ANDROID-COMPAT.md) — redroid rationale, opt-in posture, and the full honest bucketed list of what Android compat can't do (including *why* ride-hailing apps are a hard no even with a working location feed)
- [`backend/services/telephony`](../backend/services/telephony) — the hardware-gating (`IsAvailable()`), owner/user-scoping, and `mmcli` shell-out style this service mirrors
- [`clients/android/DECISIONS.md`](../clients/android/DECISIONS.md), [`clients/android/HANDOVER.md`](../clients/android/HANDOVER.md) — MOB-01…07, thin-client model, opt-in/revocable sensor-permission posture
- [`DEFAULT-WEB-APPS.md`](DEFAULT-WEB-APPS.md) — `apps/maps`'s existing in-browser `navigator.geolocation` "locate me," unaffected by this service
- [`OFFLINE-AUTH.md`](OFFLINE-AUTH.md) — the fail-closed, opt-in permission posture this doc follows
