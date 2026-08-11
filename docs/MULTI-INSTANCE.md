# Running more than one box

Vulos is built so a person can own several machines and have them behave like
one. A reminder set on the box in your study appears on the one in your office.
Your settings follow you. Your account is the same account everywhere.

This page is how you set that up, what actually replicates, and — the part most
guides skip — what deliberately does not, and why.

---

## What you get, honestly

Two boxes running this configuration converge on the same data for the domains
listed below. They are **near-identical clones, not byte-identical machines**,
and the gap is deliberate:

| Replicates | Why it should |
|---|---|
| Your account (`users`) | So you log into any of your boxes with the credentials you set once. |
| Settings (`profiles`) | Theme, locale, timezone, display name, AI provider/model, preferences. |
| Reminders | A reminder is only useful if it fires wherever you are. |
| Security audit log | Grow-only: entries can be added everywhere and edited nowhere. |

| Stays local | Why it must |
|---|---|
| Sessions | A session is a bearer token. Replicating one means a stolen cookie works on every box in the fleet. |
| Master-key and recovery blobs | The point of an enveloped key is that it exists in few places. |
| Local API keys | A credential store. |
| Push subscriptions | Per-device endpoints; meaningless on another machine. |
| Device PIN, AI API key | A PIN belongs to the machine you are standing at, not to your account. An API key is a bearer secret that can be spent. |
| Storage mode, cgroup slices | These describe **this machine's** disks and CPU budget. Copying them onto a different machine describes hardware that is not there. |

Two of those are worth being blunt about, because they are the reason "exact
clone" is the wrong goal:

- **Hardware facts cannot be cloned.** A box with a 500 GB SSD and a box with a
  4 TB array do not have the same storage layout, and pretending otherwise
  produces a machine that describes itself incorrectly.
- **Per-device credentials should not be cloned.** They exist to be true of one
  machine. A PIN that unlocks every box you own is a different security promise
  from the one the user made when they set it.

Everything else is either replicated today or named in
`backend/internal/crdtsync/policy.go` with the reason it is not — that file is
the source of truth, and it records refusals as deliberately as approvals.

### One residual you should know about

Your password hash replicates. That is what makes an account usable on a second
box, and bcrypt is designed to be stored — its cost factor is the defence, and
it does not weaken with the number of copies.

But it does mean **more machines a hash can be stolen from**. If one of your
boxes faces the internet, that box becomes the weakest link for every account in
the fleet. Use a strong password; the protection is the same one that always
applied.

---

## Setting it up

### Requirements

- Two or more boxes **on the same LAN**. WAN sync exists but needs peer identity
  configured (see below); LAN is the supported path today.
- The same person owning all of them. This is a personal fleet, not multi-tenancy.

### 1. Enable the LAN layer

Sync rides the same LAN-only listener the peering fabric uses, so it runs only
where that runs. On **every** box:

```sh
VULOS_LAN_ENABLE=1
```

This is on in the shipped systemd unit and off for a bare `vulos-server`
process — so if you are testing by running the binary directly, sync will be
dormant and nothing is broken.

### 2. Set one shared fabric secret

Boxes authenticate to each other on the LAN with a shared secret. Generate one
**once**, and put the same value on every box:

```sh
head -c 32 /dev/urandom | base64
```

```sh
VULOS_FABRIC_SECRET=<the value you just generated>
```

Without it, fabric stays **off** rather than opening an unauthenticated
exchange endpoint. That is the intended failure direction: no secret, no sync,
never "sync without authentication".

> Every box must have the **same** secret. Different values mean the boxes see
> each other and refuse each other, which looks like nothing happening.

### 3. Seal the fabric key at rest

The per-instance signing key is encrypted at rest with a keyring root key:

```sh
VULOS_FABRIC_KEY_HEX=<64 hex characters>   # 32 bytes
```

```sh
head -c 32 /dev/urandom | xxd -p -c 64
```

This one is **per box** — it is not shared, and it must not be. In production
(`--env prod`) an unset value is refused rather than falling back to a
development key.

### 4. Start them and check

Restart each box. In the logs you should see the LAN layer come up and the sync
engine register. On a box with no peers yet, that is all you will see — discovery
is mDNS, so peers appear as they start.

To confirm it is actually working, change something on one box and look for it on
the other:

1. Set a reminder on box A.
2. Wait a few seconds.
3. It appears on box B.

If it does not, work through the checks below rather than guessing.

---

## When it is not working

**Nothing happens at all.**
Check `VULOS_LAN_ENABLE=1` is set on both boxes and that you restarted after
setting it. A bare `vulos-server` run does not enable it.

**Logs say fabric is disabled.**
`VULOS_FABRIC_SECRET` is unset. This is fail-closed by design.

**Boxes can see each other but nothing syncs.**
The secrets differ. They must be byte-identical on every box.

**One box syncs and the other does not.**
mDNS does not cross subnets or most VLANs. Both boxes must be on the same
broadcast domain.

**Settings sync but a device PIN does not.**
That is correct — see the table above.

---

## Beyond the LAN

WAN sync is implemented but requires **peer identity**, and it fails closed
without it: a WAN peer with no pinned key is skipped, never dialled with the LAN
shared secret.

The reason is worth understanding rather than working around. The LAN secret says
"a member of this fleet" and never *which* member. It is a bearer token disclosed
to whatever address is dialled, and a rendezvous relay's address answer is
unsigned. That is defensible inside a link-local tunnel and is not an identity.
Over the WAN, a peer is its Ed25519 public key — the same per-instance key
already in the roster — and both requests and responses carry signed envelopes,
because a pull *response* is data your box merges into its own database.

Until you have configured that, keep your fleet on one LAN.

---

## What has actually been tested

Convergence is proven by test across simulated nodes with reordered and
duplicated delivery and an offline node catching up, and the merge is
mutation-tested. **It has not been run against two physical machines over real
mDNS** — the tests drive the real handlers over local HTTP. Clock skew and
packet loss on a real network are not covered by that.

Treat a two-box setup as working-but-young, and keep backups
([BACKUP-RECOVERY.md](BACKUP-RECOVERY.md)) as you would anyway.

---

## Related

- [ARCHITECTURE.md](ARCHITECTURE.md) — how sync fits the rest of the system
- [PEERING.md](PEERING.md) — the fabric transport this rides on
- [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) — replication is not a backup
- `backend/internal/crdtsync/policy.go` — the per-domain decisions, with reasons
