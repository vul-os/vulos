# Database migrations & schema upgrades

Vulos OS stores its control-plane state in a handful of small SQLite databases
under `~/.vulos/db/` (pure-Go `modernc.org/sqlite` — never CGo). Every one of
them is created and evolved through **one** runner: `backend/internal/migrate`.

Self-host and Vulos-managed boxes run the **identical** path — the same embedded
`.sql` files, the same runner, the same bookkeeping table. There is no separate
"production" migration story.

## The model in one paragraph

Each subsystem ships its schema as plain `*.sql` files embedded next to its code
in a `migrations/` directory, named `NNNN_short_name.sql` (zero-padded,
ascending). The baseline is `0001_*.sql`. **To change the schema you add a new
file with the next number — you never edit a file that has already shipped.**
On boot the runner applies any not-yet-applied files in filename order, each in
its own transaction, and records it in the `schema_migrations` table. That is the
whole upgrade model: forward-only, one file per change, applied in order.

## Where the databases live

| DB | Owner package | Migrations |
|---|---|---|
| `vulos.db` | `services/store` | `services/store/migrations/` |
| `auth.db` | `services/auth` | `services/auth/migrations/` |
| `files.db` | `services/files` | `services/files/migrations/` |
| registry | `internal/multiinstance` | `internal/multiinstance/migrations/` |
| cgroups | `internal/cgroups` | `internal/cgroups/migrations/` |
| llmuxclient | `internal/llmuxclient` | `internal/llmuxclient/migrations/` |

Each subsystem's `openDB` (or `store.Open`) calls
`migrate.Apply(db, migrationsFS, "migrations")` after opening the connection.

## Guarantees (from `internal/migrate`)

- **Forward-only & ordered** — files apply in ascending filename order. Zero-pad
  the number so ordering is stable (`0002_…`, not `2_…`).
- **Exactly-once & version-tracked** — every applied file is recorded by name +
  SHA-256 checksum in `schema_migrations`, so it is skipped next boot. This is
  what makes a *non-idempotent* future migration (a real `ALTER`) safe to ship.
- **Transactional & fail-closed** — each migration runs in its own transaction
  together with the bookkeeping insert. On any error the whole migration rolls
  back and boot **aborts** — the box never runs on a half-migrated database.
- **Tamper-evident** — if a file that was already applied no longer matches its
  recorded checksum, the runner refuses to start. Editing shipped history is a
  foot-gun (self-host and managed boxes would silently diverge); the runner
  forces you to add a *new* migration instead.

## How to add the next migration

1. Create `NNNN_short_name.sql` in the owning subsystem's `migrations/` dir with
   the next number (e.g. `0002_add_widget_flag.sql`).
2. Write forward-only DDL. Prefer additive changes (`CREATE TABLE`,
   `ADD COLUMN`, `CREATE INDEX`). Keep `IF NOT EXISTS` on `CREATE` where it makes
   the file safe to re-read.
3. Do **not** touch `0001_initial.sql` or any already-shipped file.
4. Ship. On next boot the runner applies only the new file and records it.

That's it — no manual "up"/"down" steps, no version numbers in Go code.

## Why the baseline is a single `0001_initial.sql`

The system was consolidated while still **greenfield** (no deployed
databases). The original incremental chains were *folded* into one clean
`0001_initial.sql` per subsystem that expresses the final shape directly — no
leftover `ALTER`/`DROP`/rename churn from evolving a live DB. The fold is proven
**schema-equivalent** to applying the original chain in order by the
`TestMigrationFold_SchemaEquivalent` tests in `services/files` and
`internal/multiinstance` (they apply the preserved legacy chain and the folded
baseline and assert an identical schema fingerprint — see
`migrate.SchemaFingerprint`). From `0002` onward, history is append-only again.

## Operator commands

```sh
# Apply pending migrations to auth.db + files.db out-of-band (idempotent):
vulos migrate up

# Show which expected tables are present:
vulos migrate status
```

`vulos.db`, the registry, cgroups and llmuxclient databases migrate
automatically when the server opens them at boot; `vulos migrate up` covers the
two standalone databases an operator may want to pre-provision.

## Recovering from a failed migration

Because apply is fail-closed and transactional, a failed boot leaves the DB at
its **previous** consistent version (the failing migration rolled back, nothing
recorded). Fix the offending `.sql` file — for an *unreleased* migration you may
edit it in place; for one that has already shipped to any box, ship a *new*
corrective migration instead — and reboot.
