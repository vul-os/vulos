// Package web embeds and serves the vulos-management authed SPA (sign-in +
// console) that lives alongside this file as a Vite project. The built assets
// (web/dist) are compiled into the binary via go:embed, so a self-hoster gets
// the React console from the single management binary with no extra static
// hosting — served on the same origin as the JSON API (see web/serve.go).
//
// COMMERCIAL SEAM. The SPA imports all commercial UI through the swappable
// '@vulos/commercial-seam' Vite alias (see web/SEAM.md). In THIS (OSS) build
// that resolves to a NoOp module — no billing/pricing UI is present in the
// bytes embedded here. The commercial distributor rebuilds the SPA with the
// alias pointed at its own implementation and embeds a different web/dist; this
// Go package is unchanged. This mirrors the Go billingport/storageport seams.
//
// EMBED CONTRACT. web/dist MUST contain index.html for the embed to compile and
// the console to serve. It is committed to the repo so `go build ./...` is green
// without a Node toolchain; rebuild it with `npm --prefix web ci && npm --prefix
// web run build` (or `make console`) after changing anything under web/src.
package web

import "embed"

// distFS holds the built SPA. `all:` so hashed assets and any dotfiles Vite
// emits are included. fs.Sub(distFS, "dist") in serve.go roots it at dist/.
//
//go:embed all:dist
var distFS embed.FS
