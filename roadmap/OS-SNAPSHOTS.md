# OS Snapshots — point-in-time restore points for a box

Status: **built.** Lives in the OS (`vulos`), package `backend/services/snapshot`
plus the admin HTTP surface in `cmd/server/routes_snapshots.go`. Not a separate
repo — the box and its bucket are the unit, and everything a snapshot needs
(the per-box object store, the cp metering seam, the admin gate) already lives
here.

A snapshot is a **point-in-time capture of the box's bucket state** (its data
and config, as it lives in object storage). Snapshots are stored **inside the
box's own bucket** under a reserved prefix, so they work identically for a
provisioned box (Vulos-supplied bucket) and a bring-your-own-bucket box — there
is no side channel and no second store to configure. Restore rolls the box back
to a chosen snapshot, fail-closed, after verifying integrity end to end.

---

## Why it lives in the OS, not a new repo

- **The box + its bucket is the unit.** A snapshot is just a second view of the
  same bucket the box already reads and writes through
  `internal/storage.Resolver` / `Resolution`. Putting snapshots anywhere else
  would re-introduce a provider dependency the resolver exists to abstract away.
- **The object-store abstraction is already here.** `storage.Resolution` +
  `minio-go` already back Files, the grant broker, and cluster sync. Snapshots
  build on the same `Resolution`, so they run over whatever bucket the box uses
  (MinIO, S3, Wasabi, Tigris, BYO) without hardcoding a provider.
- **Metering + admin gate are already here.** `internal/cpbilling` meters every
  billable surface; the auth store gates every destructive admin route. Snapshot
  storage is metered through the same seam and restore is gated the same way.

This is distinct from the existing **DB** snapshot (`services/sync` Compactor /
Restorer, `cmd_backup.go`), which captures only the SQLite control DB. OS
snapshots capture the **bucket** — the user/app data plane — and are content
addressed and incremental rather than a whole-DB image.

---

## The format (efficient by construction)

A snapshot is a **manifest of object-versions over a content-addressed blob
store**, all inside the box's bucket under a reserved artifact prefix
(`<dataPrefix>_snapshots/`, excluded from capture so snapshots never snapshot
themselves):

```
<dataPrefix>_snapshots/
  blobs/<ab>/<sha256>      # gzip-compressed object content, addressed by content hash
  manifests/<id>.json.gz   # gzip: full list of {key, size, sha256} for the snapshot
  index/<id>.json          # snapshot metadata (id, created_at, parent, kind, sizes, manifest hash)
```

- **Incremental.** Blobs are addressed by the SHA-256 of their content. A new
  snapshot re-walks every live object but only *uploads* blobs whose content is
  not already present. An unchanged object across snapshots resolves to the same
  blob and is skipped — only changed/new objects cost bytes. The manifest is
  always a complete object list (a full manifest-of-versions), so every snapshot
  is independently restorable even though the data between them is shared.
- **Compressed.** Blob content and manifests are gzip-compressed at rest.
- **Deduped.** Identical content — the same file in two places, or the same file
  across many snapshots — is stored exactly once (one blob per distinct hash).

This is the restic-style design reduced to per-object granularity: no naive
whole-bucket copy, and shared history across the retained snapshot set.

---

## Restore (fail-closed, reversible)

Restore is the sharp edge; every safeguard exists for it.

1. **Verify BEFORE touching anything.** Load the index, fetch the manifest, and
   check the manifest's SHA-256 against the hash recorded in the index. Then, for
   **every** manifest entry, confirm its blob exists AND that the blob's
   decompressed content hashes to the expected value. Any mismatch, any missing
   blob, any unreadable manifest → **abort**, box untouched. No partial state is
   ever written on a failed verification.
2. **Snapshot the current state first.** Before applying, a `pre-restore`
   snapshot of the live bucket is taken, so a bad or unwanted restore is itself
   reversible — you can restore back to where you were.
3. **Apply, then reconcile.** Each verified blob is written to its live key;
   then live objects that are NOT in the manifest are deleted, rolling back
   additions made since the snapshot. The box ends exactly at the snapshot state.
4. **Path-traversal proof.** Every manifest key is validated: no `..`, no
   absolute paths, no NUL, must re-join cleanly under the box's data prefix, and
   may never point at the reserved `_snapshots/` area. A crafted manifest cannot
   cause a write outside the box's own data scope. Blob ids must be lowercase
   hex. A snapshot is scoped to one bucket + one prefix; it can never address
   another account's or box's data.

---

## Scheduling + retention

- **Manual "snapshot now"** and **scheduled** snapshots share one code path;
  they differ only in the recorded `kind`.
- **Retention** is grandfather-father-son: keep the most recent snapshot for
  each of the last N days and each of the last M weeks, always keeping the
  newest. Everything else is pruned.
- **Pruning is safe by construction.** After deleting pruned indexes+manifests,
  a mark-and-sweep GC collects blobs no longer referenced by ANY surviving
  manifest. If any surviving manifest cannot be loaded, GC **aborts** rather than
  delete a blob it cannot prove is unreferenced (fail-closed).

---

## Metering

Snapshot storage counts against the account's storage. `StorageUsage` reports
the exact bytes held under the snapshot prefix (blobs + manifests + indexes),
broken down and totalled. On a provisioned box the delta is reported to cp via
the usage seam (`internal/cpbilling`, product `storage`, kind `snapshot_bytes`);
on a BYO-bucket box the bytes simply live in the customer's own bucket at their
provider's cost. Either way the owner can see snapshot storage via
`GET /api/admin/snapshots/usage`.

---

## Security posture

- **Owner/admin only.** Every snapshot route is admin-gated; restore and prune
  are additionally confirm-gated (`{"confirm":"RESTORE"}`), the same
  destructive-action discipline as the DB restore route.
- **Audited.** Snapshot, restore, and prune are written to the exec audit trail
  with actor + IP.
- **No cross-box/account leak.** A snapshot is the box's own bucket+prefix only;
  keys are validated to stay within it.
- **Fail-closed everywhere.** Integrity failure aborts restore; unprovable blob
  references abort GC; a crafted manifest is rejected before any write.

---

## HTTP surface (all admin-gated)

```
POST /api/admin/snapshots               — snapshot now
GET  /api/admin/snapshots               — list snapshots (+ sizes, kinds, parents)
GET  /api/admin/snapshots/usage         — snapshot storage usage (metered figure)
POST /api/admin/snapshots/prune         — apply retention now
POST /api/admin/snapshots/{id}/restore  — restore (confirm-gated)  {"confirm":"RESTORE"}
```

Wired only when the box has an object store configured; otherwise the routes
report unavailable (503), exactly like the DB backup routes.
</content>
</invoke>
