# Contributing to Vulos Management

Thanks for considering a contribution. This is the OSS (MIT), operational
control plane for Vulos — the same code a self-hoster runs and the same code
the commercial `vulos-cloud` build imports as a library. Keep that dual
audience in mind: changes here should make sense with **no** billing provider
and **no** managed storage wired in.

## The one rule that matters

> **If a self-hoster needs it to run their deployment → it belongs here. If it
> exists only because we charge money → it belongs in the private
> `vulos-cloud` repo.**

Concretely:

- Never add a hard dependency on a payment processor, a specific object-storage
  vendor, or any other commercial SaaS to a package in this module. Route
  billing/entitlement decisions through `pkg/billingport` and bucket
  provisioning through `pkg/storageport` — never import an implementation
  directly.
- `internal/archtest` enforces this mechanically: `go test ./internal/archtest/...`
  fails the build if any package in this module ever imports the private
  `vulos.cloud/cp` module. Keep it green.
- `pkg/idp/boundary_test.go` keeps the identity/login boundary minimal — it
  must not transitively import `billingport`, `superadmin`, or `orgadmin`.
- If your change requires a commercial capability to be *useful*, it's still
  welcome here as long as the **default** (no-op / BYOB) path stays fully
  functional and no package requires a real provider to compile, start, or
  pass tests.

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the full shape of the
split and the two seams.

## Getting started

Requirements: **Go 1.26+**. Postgres is optional — the control plane runs on
local SQLite with zero configuration.

```sh
git clone https://github.com/vul-os/vulos-management.git
cd vulos-management
make build   # ./bin/cp
make dev     # build + run with VULOS_ENV=local (bare `./bin/cp` fails closed — see below)
make test    # go test ./...
make vet     # go vet ./...
```

> `./bin/cp` on its own refuses to start (`SESSION_SECRET is unset in prod`) —
> that's the prod fail-closed guard working correctly, not a bug. `make dev`
> sets `VULOS_ENV=local` so it boots with a dev fallback secret instead. See
> [`docs/SELF-HOST.md`](docs/SELF-HOST.md#a-note-on-vulos_env).

See [`docs/SELF-HOST.md`](docs/SELF-HOST.md) for the full configuration
surface and a breakdown of what `cmd/server` wires in today versus what's
still pending a `RouteRegistrar`.

## Before opening a PR

There is currently **no CI** on this repo (no `.golangci*`, no
`.github/workflows/`) — see [`docs/SELF-HOST.md`](docs/SELF-HOST.md#build-hygiene-no-ci-no-lint-config).
Nothing enforces the checks below automatically; you are expected to run them
by hand before every commit/PR. All of them are green as of this writing —
keep them that way:

```sh
go build ./...
go vet ./...
go test ./...
gofmt -l .        # should print nothing
```

If you touched `web/` (the console SPA), also run:

```sh
cd web && npm run lint   # eslint . — should exit 0 (warnings are tolerated, errors are not)
```

- Keep the module building and testing green with **no** environment
  variables set beyond what a bare self-host deployment needs (`make dev`,
  i.e. `VULOS_ENV=local` and nothing else).
- Add or update tests alongside behavior changes — most packages here carry
  both an in-memory/SQLite test path and a `*_pg_test.go` Postgres path
  (skipped automatically when no test Postgres DSN is configured).
- If you touch a store, keep the SQLite and Postgres migration dialects in
  lock-step — several packages assert this with a schema-equivalence test.

## Branching and commits

- Branch off `main`. Prefixes like `feat/…`, `fix/…`, `docs/…`, `refactor/…`
  are welcome but not enforced.
- Keep PRs focused on one logical change; it makes review and rollback easier.
- Write commit messages and PR descriptions that explain **why**, not just
  what — especially for anything touching auth, the admin console gate, or a
  seam boundary.

## Reporting bugs and requesting features

Open a GitHub issue. For anything security-sensitive, **do not** open a public
issue — see [`SECURITY.md`](SECURITY.md) instead.

## Code of conduct

Be respectful and assume good faith. Disagreements about architecture or
scope are fine and expected — keep them focused on the code and the two-repo
line, not the person.
