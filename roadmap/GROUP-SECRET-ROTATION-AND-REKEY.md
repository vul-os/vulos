# Group secret rotation, and the re-key it does not perform

**FABRIC-SECRET-ROT-01.** Status: **rotation BUILT and tested. Group re-key
SPECIFIED, not built.** That split is the honest deliverable and this document
exists to keep the two apart, because "re-key" is a word that implies more than
what shipped.

This closes gap 2 of the three the eviction work left open, and specifies gap 1.

---

## 1. Where the secret lived, established from the code first

`VULOS_FABRIC_SECRET` is an **environment variable read once at startup**. Not a
file re-read on change — nothing watches it, nothing reloads it, and no handler
can write it.

`cmd/server/main.go` reads it during boot and copies the resulting string into
three places that then hold it, immutable, for the life of the process:

| Holder | What it does with it |
|---|---|
| `fabric.Config.Secret` | compared by `Service.authOK` on every inbound `/api/fabric/{changeset,status}`, and sent as `X-Fabric-Auth` on every outbound one |
| `crdtsync.SyncerConfig.Secret` | sent as `X-Fabric-Auth` to LAN peers (`syncer.go`'s `post`) |
| `crdtsync.SecretAuthorizer(secret)` | a closure over the same string, gating `/api/crdt/{pull,push,sync-status}` |

**One acceptance slot.** That shape has exactly one rotation mechanic available
to it — change the variable and restart — and the moment the first box restarts
with a new value it stops being able to talk to every box that has not restarted
yet. A fleet updates one box at a time, so "rotate the secret" meant "partition
the fleet, then unpartition it".

That is the whole reason there was no tooling. The missing piece was never a
script; it was a **second acceptance slot**.

### What changed

The single string becomes `fabric.SecretRing` (`internal/fabric/secretring.go`):

| Variable | Role |
|---|---|
| `VULOS_FABRIC_SECRET` | the **current** secret. Accepted inbound, and the **only value ever sent** outbound. |
| `VULOS_FABRIC_SECRET_ALSO` | an **additional accepted** secret. Inbound only. **Never transmitted.** |
| `VULOS_FABRIC_SECRET_ALSO_UNTIL` | RFC3339 **absolute instant** after which the `ALSO` slot is refused. Required whenever `ALSO` is set. |

The ring is what both doors consult:

- `fabric.Service.authOK` → `ring.Accepts(presented)`
- `crdtsync.RingSecretAuthorizer(ring)` replaces `crdtsync.SecretAuthorizer(str)`
  at the wiring site in `cmd/server/crdtsync_wiring.go`.

Outbound is unchanged and deliberately so: a box always sends its **current**
secret. An overlap widens what a box *admits*, never what it *discloses*, so a
rotation cannot leak the new secret to a peer that was not given it out of band.

The slot is named `ALSO`, not `PREV`. A safe roll needs it to hold the **new**
secret in phase 1 and the **old** secret in phase 2 (below); a name saying
"previous" would push an operator toward a one-phase roll, which partitions the
fleet exactly as before. It is directional only in that it is never transmitted.

---

## 2. The overlap window, and exactly how it closes

Two secrets valid at once is the obvious mechanism and is also **how a rotation
becomes permanent**. An overlap nobody closes is just two secrets.

### The roll

Let `O` be the old secret, `N` the new one.

```
PHASE 1 — PREPARE.  On every box, one at a time:
    VULOS_FABRIC_SECRET=O   VULOS_FABRIC_SECRET_ALSO=N   ..._UNTIL=<deadline>

  Every box still SENDS O, which every box still accepts. There is no instant at
  which a prepared box cannot talk to an unprepared one.

PHASE 2 — COMMIT.  On every box, one at a time:
    VULOS_FABRIC_SECRET=N   VULOS_FABRIC_SECRET_ALSO=O   ..._UNTIL=<deadline>

  A committed box sends N; an uncommitted box is still in phase 1 and accepts N
  in its ALSO slot. An uncommitted box sends O; a committed box accepts O in its
  ALSO slot. Every pair works in both directions throughout.

PHASE 3 — CLOSE.  Let the deadline pass, or remove the two variables.
```

One extra slot is sufficient for a fleet of any size **because the slot changes
role between the phases**, not because two secrets are enough at any one instant.

### The closing, which is the part that has to be as testable as the opening

Three mechanisms close it, and the first needs nobody:

1. **The deadline, evaluated per request against the wall clock** — not at load
   time. A box running since before the deadline starts refusing the old secret
   the moment it passes, with **no restart and no operator action**. This is what
   makes the close as testable as the open: the same ring, the same presented
   secret, two clock readings, two answers.
2. **The hard cap.** `MaxSecretOverlap` (7 days) clamps a deadline further out,
   so a mistyped year cannot turn the overlap into a permanent second secret.
3. **The operator** removing the variable.

The deadline is an **absolute operator-supplied instant**, not `now + 24h`
measured from process start. A process-relative window is renewed by every
restart, so a box that restarts daily would hold it open forever while every log
line claimed a 24-hour overlap. This is the single most important decision in
the design and it is the one that is easiest to get wrong.

### Configurations that would make the overlap permanent, and what they do instead

Every one resolves to the **narrower** ring — never wider, and never a hard
error, because a box mid-roll with a bad deadline must fall back to accepting one
secret (the state it was in before rotation existed) rather than refusing to
boot. All of them also **warn**: an operator meeting a silent 401 mid-roll has
nothing to go on.

| Input | Result |
|---|---|
| `ALSO` set, `_UNTIL` missing | slot **closed** + warning. An unbounded overlap is not a rotation. |
| `_UNTIL` unparseable | slot **closed** + warning. The current secret is unaffected — a bad deadline must not be an outage. |
| `ALSO` == `VULOS_FABRIC_SECRET` | slot **closed** + warning. Presenting the same value twice is not a rotation. |
| `ALSO` set, no current secret | slot **closed** + warning. |
| `_UNTIL` in the past | slot **closed**, **no warning** — this is the normal *end state* of a roll, not a misconfiguration. |
| `_UNTIL` beyond `MaxSecretOverlap` | **clamped** + warning. |

### Why not a generation counter

The brief offered "a generation counter that refuses the old one once every peer
has been seen on the new". It was considered and rejected, and the reason is
structural rather than a preference: **the shared secret cannot attribute a
peer.** That is its defining weakness and the entire reason the Ed25519 signature
path exists. "Every peer" is not a set this credential can enumerate. Counting
*distinct secrets seen* would be counting connections, and one box reconnecting
twice would close the window on the fleet.

What replaced it is the honest approximation — see the status surface below.

---

## 3. Who may rotate

**The operator, on the box, out of band. There is no rotation endpoint, and
`SecretRing` exposes no mutator: its policy is immutable after construction.**

Two independent reasons, either sufficient:

1. A rotation endpoint any peer could call is a **fleet-wide denial-of-service
   primitive**. This matches the standing posture: `/api/apps/launch` is
   admin-only, process-kill is admin-only, and `POST /api/setup/complete` is
   owner-only *by design* because a route that ends setup can skip it.
2. **Even owner-gated it would be wrong, and this is the decisive one.** An HTTP
   rotation would have to *distribute* the new secret to the other boxes, and the
   only channel it has is the fabric — which the box being evicted is **still
   authenticated on for the whole overlap**. You cannot hand out a new group
   secret over a channel the evicted member can still read. Distribution must be
   out of band, which means the operator, which means an environment variable and
   a restart.

`TestFabricRegistersNoRotationEndpoint` is the regression gate: it asserts the
fabric service registers exactly its three documented endpoints, so adding a
network-reachable rotation route trips a test that points at this reasoning.

Note the asymmetry that makes this coherent: **rotation needs a restart, but the
close does not.** Widening what a box accepts is an operator act with physical
access; narrowing it happens on the clock. That is the correct way round.

---

## 4. The status surface: which secret is this box on, and has every peer moved?

A rotation an operator cannot verify is one they will not run, and an unrun
rotation is the same as no rotation. `GET /api/fabric/status` (already gated on
the secret) answers the only two questions an operator actually has:

**"Which secret is this box on?"** — `authenticated_with` reports the slot **the
caller's own header** matched: `"current"` or `"overlap"`. Present the new secret
to each box in turn: `current` means that box has **committed** (phase 2 done),
`overlap` means it has only **prepared** (phase 1). This discloses nothing — the
box is describing a value the caller just sent it.

**"Has every peer moved?"** — answered as honestly as an unattributable
credential permits, which is **not by naming peers**. The `secret_rotation`
object reports `overlap_configured`, `overlap_open`, `overlap_closes_at`,
`admitted_on_current`, `admitted_on_overlap`, `overlap_first_used_at` and
`overlap_last_used_at`.

`overlap_last_used_at` is the number to watch: **while it advances, some box has
not been rolled** and closing the window would partition it off; when it stops
advancing, the roll is done. It cannot say *which* box and does not pretend to.
Any per-peer attribution here would be invented rather than measured.

**No digest or fingerprint of either secret is published.** A digest of a
low-entropy shared secret *is* the secret, and this endpoint is readable by a
caller holding only the old one.

`Accepts` (the door, counts) and `Slot` (inspection, counts nothing) are split so
the measurement cannot corrupt itself — see §7.

---

## 5. What is and is not re-keyed

This is the section most at risk of being read as a larger claim than it is.
Three different pieces of key material are in scope of the phrase "group
re-key", and **this work moves exactly one of them**.

### (a) `VULOS_FABRIC_SECRET` — the group bearer secret. **ROTATABLE NOW.**

The fleet-wide symmetric credential gating `/api/fabric/changeset`,
`/api/fabric/status` and (during the bootstrap window) `/api/crdt/*`. It can now
be changed on a live fleet without an outage, and the old value stops being
accepted at a deadline that needs no operator to return.

**What that is worth against an evicted box:** after the window closes, the
evicted box's copy of the old secret opens nothing. That is a real reduction and
it is the *only* one this work delivers.

**What it is not:** it is not retroactive. The evicted box keeps every byte it
already read. And the operator must not hand the new secret to the evicted box —
which is trivially true here because distribution is manual, and is exactly why
an HTTP rotation route would have been self-defeating (§3).

### (b) The SSE-C bucket key. **NOT re-keyed. Not even rotatable.**

`services/cluster` derives a 256-bit AES key as
`Argon2id(clusterPassphrase, salt)` where the salt is a single object at
`cluster/encryption-salt` **inside the bucket itself** (`s3.go`'s
`fetchOrCreateSalt`). Every object is encrypted under that one key with SSE-C,
which means the store holds no key material — the key travels on each request.

Changing either input yields a different key, under which **every existing object
becomes undecryptable**. There is no epoch, no per-object key id, and no
re-encryption path. So:

- An evicted box that held the cluster passphrase can still decrypt **every
  object it can reach**, before and after this change.
- Rotating the fabric secret does nothing about this. They are unrelated
  credentials with unrelated distribution.

This is specified in §6 and is the larger half of the remaining work.

### (c) Per-instance Ed25519 identities. **DELIBERATELY NOT TOUCHED.**

`multiinstance.RotateIdentity` + `LoadOrCreateSealedInstanceKey` already own
rotation for these, with their own overlap window
(`prev_ed25519_public_key` / `prev_key_expires_at`, `DefaultRotationOverlap`),
their own revocation, and the monotonicity fixes the eviction work landed
(`RotateIdentity` refuses to rotate while self is revoked; `Registry.Upsert`
latches the revoked bit). `services/devicekey` owns the revocation store.

**Sweeping these into a "group re-key" would be a regression, not a bonus.** A
per-instance identity is the one thing that must *differ* between boxes; it is
what quorum, attribution and eviction all depend on. A fleet-wide operation that
touched every box's identity at once would destroy exactly the asymmetry that
makes eviction possible. They are correct as they are and are out of scope by
design, not by omission.

### Summary table

| Material | Scope | Rotation before | Rotation now | Re-keyed by this work |
|---|---|---|---|---|
| `VULOS_FABRIC_SECRET` | fleet-wide symmetric | **none** | two-phase overlap roll, deadline-closed | **yes** |
| SSE-C bucket key (`Argon2id(passphrase, salt)`) | fleet-wide symmetric | none | none | **no** |
| Per-instance Ed25519 signing key | per box | `RotateIdentity`, overlap + revocation | unchanged | **no — correctly** |
| Peer roster / revocation set | per box, monotonic | `VULOS_FABRIC_REVOKED_PEERS` | unchanged | n/a |

---

## 6. The group re-key: specified, not built

What follows is design. **None of it is implemented.** It extends §3.4 of
`SYNC-INVENTORY.md` ("Epoch the group keys, because some must remain shared")
with the piece that document left open, now that a rotation mechanism exists to
build it on.

### Why rotation had to come first

An epoched group key is a *distribution* problem wearing a *cryptography*
costume. The hard part is not generating a fresh key; it is getting it to the
remaining members without giving it to the evicted one, and doing so while the
fleet keeps working. That is precisely the problem the two-phase overlap roll
solves for a symmetric credential, and the re-key needs the same shape:

- an epoch that both old and new are valid across (the overlap),
- a definite close (the deadline),
- and a distribution channel the evicted member cannot read.

### The design

1. **Epoch number**, monotonic and grow-only, on the group key. Follow
   `services/signing/epoch.go`, which already implements a monotonic floor — a
   compromised peer must not be able to roll the fleet back to an epoch it holds
   the key for.
2. **Eviction increments the epoch** and mints a fresh key.
3. **The new key is wrapped once per remaining member**, to that member's
   *device public key* — which is the per-instance Ed25519 identity of §5(c),
   used here as a **wrapping target only, not rotated**. This is the same
   wrapping infrastructure the password vault needs, which is why
   `SYNC-INVENTORY.md` §4 says to build them together.
4. **New writes use the new epoch. Old objects stay readable at their old
   epoch** until re-encrypted lazily. For SSE-C this means an object needs an
   epoch tag in its metadata and the client needs to hold the key for every
   epoch it might read — a per-object key id, which `services/cluster` does not
   have today and which is the concrete first task.
5. **Salt placement must move.** The Argon2id salt currently lives *inside the
   bucket it protects* (`cluster/encryption-salt`). An epoch scheme cannot store
   epoch *N*'s salt where an evicted box with read access can fetch it. The salt
   is not secret in the cryptographic sense, but its placement means today's
   scheme has nothing an evicted reader lacks except the passphrase.
6. **Quorum for the eviction that triggers a re-key**, with the two-box case
   named: `fleetid.VerifyQuorum` for fleets that can form a majority, and
   **explicit owner authorisation with a step-up challenge** (`services/stepup`)
   for a two-instance fleet — *not* unanimity-minus-one, because "the other box
   agrees" is exactly what a compromised other box will say.

### What the re-key still could not do, and must not be described as doing

Even fully built, it bounds the **future**. The evicted box keeps every byte it
already read, and no epoch can retract that. Eviction must therefore be
*accompanied* by credential rotation for everything that box could read — user
passwords, API keys, app tokens in `profile_secrets` — and the eviction flow
should present that list rather than leaving the user to guess.

### Ordering

1. Per-object epoch tag + multi-epoch key holding in `services/cluster`. Nothing
   else is possible without it, and it is independently useful.
2. Move the salt out of the protected bucket.
3. Key wrapping to per-instance public keys (shared with the password vault).
4. Wire eviction → epoch increment, behind the quorum/step-up gate above.

---

## 7. The tests, and both directions

The test that matters is stated in both packages, and **both directions are
asserted**, because a rotation that never rejects the old value has done nothing
and would pass every "accepts the new secret" test anyone would write.

| Test | Proves |
|---|---|
| `fabric.TestOldSecretIsAcceptedBeforeTheWindowClosesAndRefusedAfter` | Over a real TLS listener, through the real handlers, on `/api/fabric/changeset` **and** `/api/fabric/status`: old secret → **200 before** the deadline, **401 after**, same process, no restart. Current secret → 200 in both eras (a rotation that takes the fleet down has rotated nothing). An unrelated secret → 401 in both eras (so the passing half is not "admits everything"). |
| `TestCRDTDoorAcceptsTheOldSecretBeforeTheWindowClosesAndRefusesAfter` | The same both-directions assertion for the *other* door, through a real `ServeMux` against the real `Authorizer` the wiring installs. |
| `fabric.TestSecretRingClosesTheWindowOnTheDeadlineWithoutARestart` | The unit-level close, **including the boundary**: open 1ns before, **closed exactly at** the instant — "until 14:00" must not mean "and also at 14:00". |
| `fabric.TestSecretRingRefusesAnOverlapThatWouldNeverClose` | Every configuration from §2's table resolves narrower, and **warns**. |
| `fabric.TestSecretRingClampsAnOverlapLongerThanTheMaximum` | A 10-year deadline is clamped, loudly, and the window still opens. |
| `fabric.TestStatusNamesTheSlotTheCallerAuthenticatedWith` | A prepared box reports `overlap` and a committed box reports `current` for the same secret — the distinction an operator closes the window on. |
| `fabric.TestStatusReportsWhetherAnybodyIsStillOnTheOldSecret` | The counters distinguish the slots, and move **exactly once per request**. |
| `TestCRDTDoorIsWiredToTheRotationRing` | The call site, via AST: the door is wired to a ring from `LoadSecretRingFromEnv`, and the syncer still sends the *current* secret outbound. |
| `TestFabricRegistersNoRotationEndpoint` | No network-reachable rotation route (§3). |

The clock is **injected**, not slept through. A test that sleeps past a real
deadline flakes on a loaded host, and the property under test is that the
deadline is evaluated *per request* — which an injected clock demonstrates and a
sleep only approximates.

### A survived mutation, and what it taught

Mutation 4 made the status handler call `Accepts` a second time, so a status poll
counted itself. **It survived.** The test polled with the *current* secret and
asserted on `admitted_on_overlap`, so the double-count landed in
`admitted_on_current`, which nothing was reading. The property was real, the test
was green, and it could not see it.

The wrong assertion looked right because it named the operationally interesting
counter while polling on the other secret, so the two halves never met. The fix
measures the counter belonging to **the secret the caller presented**, and
measures it **exactly** — "did it move at all" cannot tell one increment from
two, and one-versus-two is the whole difference between "the door counted" and
"the door counted and then the observer counted again".

The stake is not bookkeeping. An operator checking a laggard box does it with the
**old** secret. If that poll counted, `overlap_last_used_at` would advance
whenever anyone looked, the roll would never appear finished, and the window
would never be closed. **The observability would have destroyed the thing it
exists to observe.**

---

## 8. What remains designed-but-unbuilt

- **The group re-key** (§6), in full. Epoch tags, salt relocation, key wrapping,
  and the quorum-gated trigger.
- **The fabric changeset transport is still unattributable.**
  `/api/fabric/changeset` is gated on the shared secret **alone** — no signature
  path, no roster check. Rotation narrows *which* secret opens it; it does not
  make the caller nameable. That door needs the same per-peer signature treatment
  `crdtsync` received, on both `fabric/handlers.go` and `fabric/client.go`.
- **Revocation still does not propagate.** It must be entered on each box.
- **The two doors count separately.** `crdtsync_wiring.go` builds its own ring
  from the same environment as `internal/fabric`. They agree on policy (same
  input, same clock) but do not share admission counters, so
  `/api/fabric/status` reports the fabric door's traffic only. That is the right
  way round — the fabric endpoints are gated on the secret alone, whereas the
  crdtsync door consults it only during the bootstrap window — but a single
  shared ring would be strictly better and needs one line at `main.go`'s
  `startCRDTSync` call site, which was left alone to avoid a shared-file
  conflict.
- **Nothing needed in `internal/multiinstance`.** Its rotation/revocation model
  was read and is unchanged by this work; §5(c) is why.

### One stale claim in a file this work did not own

`backend/internal/sqlcrdt/osstate.go`'s inventory row for the fabric secret says
"…it cannot be revoked for one box without re-keying all of them, **and there is
no re-key path**". The first clause is still exactly right. The second is now
out of date: re-keying all of them is what §2's two-phase roll does. It was left
alone rather than edited to avoid a shared-file conflict, and the suggested
correction is:

> …it cannot be revoked for one box without re-keying all of them. Rotating it
> across the whole fleet is now possible (FABRIC-SECRET-ROT-01), but a *group
> re-key* — fresh material re-wrapped per remaining member so an evicted box is
> excluded without a manual roll — is still unbuilt.
