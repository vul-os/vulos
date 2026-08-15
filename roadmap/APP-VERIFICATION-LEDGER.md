# App Hub — install verification ledger

Generated from `roadmap/app-verification-ledger.json` by
`scripts/verify-app-recipe.sh --ledger-render`. **Do not hand-edit this file** —
edit nothing, run the harness. A row exists only because a container really ran.

`passed` = the product's own installer (`appnet.InstallFromRegistry`) ran in a
debian:trixie container, every assertion in the *Asserted* column held, and the
app was then removed again. `untestable-on-arm64` = the upstream publishes no
aarch64 build, so this machine cannot install it — that is a stated limit, **not**
a pass and **not** a claim the app works.

| status | apps |
| --- | --- |
| ✅ passed | 1 |

| App | Source | Arch | Verified | Status | Disk MB | Mins | Date | Asserted / why not |
| --- | --- | --- | --- | --- | ---: | ---: | --- | --- |
| `filezilla` | flathub | arm64 | - | ✅ passed | 2059 | 29 | 2026-08-15 | install-path, manifest-written, command-declared, flatpak-present, flatpak-deployed, flatpak-runtime, command-resolves, command-executes, uninstall |
