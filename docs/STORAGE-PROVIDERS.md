# Storage Providers — Choosing a Bucket for Your Box

**You do not need a storage provider to run a Vulos box.** By default a box
keeps its object data — Files/Drive and app storage — as plain files on its own
disk (`/var/lib/vulos/storage`), with no endpoint, no bucket, no credentials
and no third-party service involved. This guide is for the case where you
*choose* to add an **S3-compatible object store**: a second node that must
serve the same data, or an off-box copy of the bytes.

When you do, you bring your own bucket: the box only ever needs an
**endpoint**, a **bucket name**, an **access key**, a **secret key**, a
**region**, and (for the backup vault) an **encryption passphrase**. Vulos
operates no storage of its own — the store is always something you rent from a
third party or run yourself.

> **Vulos is free and open-source — we charge nothing, ever.** There is no Vulos
> subscription, licence, or pricing. Every figure in this guide is a *third-party
> infrastructure* cost you pay **directly to the storage provider you choose** (or
> nothing at all, if you self-host the store). None of it goes to Vulos.

This guide helps you pick that provider and, just as importantly, understand
where the bill comes from. Object storage pricing has a few well-known traps —
egress fees and minimum-duration charges chief among them — and this document
calls them out plainly.

> **Not sure yet?** For a single-user box the honest answer is: pick a
> zero-egress provider (Cloudflare R2, Tigris, or Backblaze B2), or run
> MinIO/Garage on the same machine and pay nothing extra. The worked example
> near the end shows why the monthly cost is usually cents, not dollars.

---

## Table of contents

1. [What Vulos stores, and how it is protected](#what-vulos-stores-and-how-it-is-protected)
2. [The two storage roles on a box](#the-two-storage-roles-on-a-box)
3. [Provider comparison](#provider-comparison)
4. [Where costs actually arise](#where-costs-actually-arise)
5. [A worked example](#a-worked-example)
6. [Honest third-party caveat](#honest-third-party-caveat)
7. [Plugging your provider into Vulos](#plugging-your-provider-into-vulos)

---

## What Vulos stores, and how it is protected

Your box writes three kinds of thing to object storage:

- **Files/Drive objects** — the files you upload through the Drive app, plus
  app data for apps that hold the `storage` permission.
- **App-storage seam data** — per-app object storage, isolated per user and (via
  short-lived STS credentials) per app. See
  [CONFIGURATION.md → Per-app isolation](CONFIGURATION.md#per-app-isolation-sts--on-by-default-for-self-host).
- **The backup vault** — periodic [Restic](BACKUP-RECOVERY.md) snapshots of the
  box's state.

**Be clear about what is and isn't encrypted before it leaves the box** — this
matters when you choose who holds the bucket:

| Data | Encrypted before it leaves the box? |
|---|---|
| **Backup vault (Restic)** | **Yes, always.** Encrypted client-side with your passphrase (`VULOS_RESTIC_PASSWORD`). The provider holds ciphertext only, and Vulos refuses to back up in production while that passphrase is still the dev default. |
| **Drive uploads to a bucket _you_ host or control** | Plaintext on the wire and plaintext in the bucket — but that bucket is on infrastructure you control, so the plaintext never leaves your hands. This is the normal self-host case. |
| **Drive uploads via a broker you don't control** (`DEPLOY_MODE=cloud` only) | **Sealed client-side by default** (VSEAL1: X25519 + AES-256-GCM) to your own content key, so the broker only ever sees ciphertext. Fail-closed: no unlocked key ⇒ the upload is refused, never silently sent in plaintext. |
| **Sealed shares** | **Yes** — encrypted to the recipient's published content key; relaying infrastructure transports ciphertext it cannot open. |

Full detail lives in [FILES.md → Storage sealing](FILES.md#storage-sealing).
The short version: **for the ordinary self-host case, your bucket sits on
infrastructure you already trust, and the backup vault is encrypted no matter
whose bucket it lands in.** Sealing (VSEAL1) is a confidentiality layer the
client can add on top of any bucket; it is not something the storage tier
guarantees on its own.

The practical consequence for provider choice: **because the provider is either
under your control or holds ciphertext, you are choosing them for
_availability, durability, and price_ — not to be trusted with your plaintext.**

---

## The two storage roles on a box

A box actually reads two independent S3 configurations, and it is worth knowing
which is which before you go shopping (they can point at the same provider, or
different ones):

| Role | Config prefix | What it holds |
|---|---|---|
| **Per-user object-store gateway** | `VULOS_STORAGE_*` (or the bundle's `storage.yaml`) | Files/Drive and app-storage data — the bucket that grows with what you keep. |
| **Backup vault** | `S3_*` + `VULOS_RESTIC_PASSWORD` | Restic snapshots for disaster recovery. |

They are deliberately separate — see
[CONFIGURATION.md → Storage: two independent S3 configs](CONFIGURATION.md#storage-two-independent-s3-configs).
Most people point both at the same provider (or the same bucket-family) for
simplicity; that is fine.

---

## Provider comparison

All of the below speak the S3 API and work with Vulos. Pick a reputable one and
verify its current pricing on its own site — see the
[caveat](#honest-third-party-caveat) below.

| Provider | Shape of the price | Egress (download) | Minimums / gotchas | Free tier | Best when |
|---|---|---|---|---|---|
| **Cloudflare R2** | Per-GB-month storage, per-request Class A/B | **Zero egress fees** | None notable | Generous monthly free storage + requests | You want zero-egress and predictable bills; the default first choice for most boxes |
| **Backblaze B2** | Cheapest per-GB-month storage | Free up to a multiple of stored volume, then per-GB | None notable | A few GB of storage free | Cheapest raw storage; big archives with modest download |
| **Wasabi** | **Flat** per-TB-month, no request fees | No egress fees (fair-use capped to your stored volume) | **90-day minimum storage duration**; **effective ~1 TB / monthly minimum charge** regardless of use | — | You store a lot (≥ ~1 TB) and delete rarely; overkill for a small box |
| **AWS S3** | Per-GB-month storage, per-request, **metered egress** | **Metered per-GB** (this is the one that surprises people) | Storage-class minimums (e.g. IA/Glacier); complex pricing | Small storage + requests, first 12 months | You already live in AWS, or want maximum ubiquity and tooling |
| **Tigris** | Per-GB-month storage, small per-request | **Zero egress fees** | None notable | Free tier available | You want zero-egress and edge-close reads; the bundle's default |
| **Self-hosted MinIO / Garage** | Your disk + your bandwidth | Your own bandwidth cost | You run it, patch it, and back it up | n/a (you own it) | Full sovereignty / air-gap; no third party at all |

**The one-line differentiators:**

- **Cloudflare R2 / Tigris** — *zero egress fees.* You are never billed for
  reading your own data back. This makes the monthly cost trivially predictable.
- **Backblaze B2** — *cheapest per-GB storage.* Egress is free up to a generous
  multiple of what you store, then metered.
- **Wasabi** — *flat, no-egress, no-request* pricing, but with a **90-day
  minimum storage duration** (delete something early and you still pay to
  90 days) and a practical **~1 TB monthly minimum** — sized for people
  storing real volume, not a 20 GB personal box.
- **AWS S3** — *ubiquitous and battle-tested,* but **egress is metered per GB**
  and the pricing has many dimensions. Easy to run up a surprising download bill.
- **MinIO / Garage (self-hosted)** — *you own it end to end.* No third party
  sees even ciphertext. The cost is the disk, the bandwidth, and your time.
  Garage is a lightweight, replication-friendly alternative to MinIO that runs
  happily on small/edge hardware.

MinIO is a first-class option in Vulos: the self-host bundle can install and
supervise it for you on loopback — see
[SELF-HOST-BUNDLE.md → Storage backends](SELF-HOST-BUNDLE.md#storage-backends).

---

## Where costs actually arise

Object-storage bills are built from a handful of independent line items. Knowing
them is the whole game:

1. **Storage — per GB-month.** You pay for the bytes you keep, prorated over the
   month. This is the obvious one and, for a personal box, usually the smallest.
   Roughly (approximate, verify current): **AWS S3** ≈ $0.023/GB-mo, **Tigris**
   ≈ $0.02/GB-mo, **Cloudflare R2** ≈ $0.015/GB-mo, **Backblaze B2** ≈
   $0.006/GB-mo, **Wasabi** ≈ $0.007/GB-mo but billed as a flat per-TB minimum.

2. **Egress / bandwidth — per GB downloaded. This is the line that surprises
   people.** Reading your data _back out_ of the store can cost more than
   keeping it there. On AWS S3, egress is roughly $0.09/GB after a small free
   allowance — download 100 GB and that is ~$9, dwarfing the storage cost.
   **Cloudflare R2, Tigris, and Wasabi charge $0 for egress**; **Backblaze B2**
   gives free egress up to a multiple of your stored volume, then meters it.
   **For a Vulos box — where you sync and re-read your own files constantly — a
   zero-egress provider is almost always the right call.**

3. **API request charges — per operation, split into classes.** Providers meter
   the number of API calls, usually as **Class A / write-heavy** (PUT, POST,
   LIST, COPY — more expensive) and **Class B / read** (GET, HEAD — cheaper),
   with DELETE typically free. Order-of-magnitude: Class A ≈ a few dollars per
   million; Class B ≈ tens of cents per million. For a single-user box this
   is usually rounding error, but a chatty app or an aggressive sync loop can
   run it up.

4. **Minimum storage duration.** Some providers bill a minimum retention no
   matter when you delete: **Wasabi = 90 days**; AWS's colder classes (IA,
   Glacier) have 30–180-day minimums plus retrieval fees. Delete an object
   early and you are still charged to the minimum. Zero-cost surprise if you
   churn data a lot.

5. **Minimum capacity / minimum monthly charge.** **Wasabi** in particular bills
   an effective **~1 TB minimum per month** — so a 20 GB box pays the same as a
   1 TB one. Great value at scale; poor value for a small personal box.

6. **Free tiers.** **Cloudflare R2** and **Backblaze B2** both offer a genuinely
   useful monthly free allowance (storage + a large number of requests, and for
   B2 a few GB of storage) that a single-user box can sit largely _inside_.
   **AWS** offers a 12-month free tier (a few GB) that then expires. **Tigris**
   offers a free tier as well. Always confirm the current numbers.

**Rule of thumb:** for a personal box, storage is cheap, requests are
negligible, and **egress is the only line item that can bite** — so a
zero-egress provider (R2, Tigris, B2) removes the one real risk of a surprising
bill.

---

## A worked example

**A single-user box: ~20 GB stored, light everyday traffic** (a few GB of
uploads/downloads a month, ordinary sync). Figures are *approximate — verify
current pricing before relying on them:*

| Provider | Storage (20 GB) | Egress | Requests | Rough monthly total |
|---|---|---|---|---|
| **Cloudflare R2** | 20 × ~$0.015 ≈ **$0.30** | $0 | likely within free tier | **~$0.30, often effectively $0** within the free allowance |
| **Tigris** | 20 × ~$0.02 ≈ **$0.40** | $0 | negligible | **~$0.40** |
| **Backblaze B2** | 20 × ~$0.006 ≈ **$0.12** | free within 3× stored | negligible | **~$0.06–0.12** (first few GB free) |
| **AWS S3** | 20 × ~$0.023 ≈ **$0.46** | + ~$0.09/GB you download | small | **~$0.50 + egress** — cheap until you download a lot |
| **Wasabi** | flat ~1 TB minimum | $0 | $0 | **~$7/month minimum** — you pay for 1 TB you aren't using |
| **MinIO / Garage** (on the box's own disk) | uses local disk | your own bandwidth | n/a | **$0 marginal** (or the cost of the VPS/disk you already run) |

**Takeaways:**

- For a small box, the cheapest hosted options cost **cents per month**.
- **Wasabi's minimums make it the wrong tool for a 20 GB box** — it shines at
  terabyte scale, not personal scale.
- **AWS is fine until download volume climbs** — its metered egress is the thing
  to watch.
- **Self-hosting MinIO/Garage on the same machine costs nothing extra** beyond
  the disk and bandwidth you are already paying for, and keeps every byte on
  hardware you own.

---

## Honest third-party caveat

- **These are independent third parties, not run by Vulos.** Cloudflare,
  Backblaze, Wasabi, AWS, and Tigris are separate companies with their own
  terms, uptime, and billing. Vulos does not operate, resell, or endorse any of
  them — it simply speaks the S3 API they expose. Choose a reputable provider.
- **Prices and free tiers change.** Every figure in this document is an
  **approximate order-of-magnitude, presented to show the _shape_ of the cost
  and the gotchas — not a quote.** Always confirm current pricing, free-tier
  limits, minimum-duration terms, and egress policy on the provider's own
  pricing page before you commit.
- **Your data is protected, but availability and billing are theirs.** The
  backup vault is encrypted with your passphrase and sealed shares are encrypted
  to their recipients, so a provider generally cannot read protected content —
  but the provider still controls **whether your data is available**, its
  **durability guarantees**, and **what they charge you.** Read their SLA and
  durability claims.
- **Self-hosting removes the third party entirely.** Run **MinIO or Garage** and
  there is no external company in the loop at all — you own the availability,
  the durability, the backups, and the bill. That is the maximally sovereign
  option, and Vulos supports it as a first-class backend.

---

## Plugging your provider into Vulos

Once you have created a bucket and an access key/secret pair with your chosen
provider, wire it into the box. There are two paths:

### Onboarding wizard (easiest)

During first boot, the setup wizard has a **Storage** step where you connect an
S3 bucket (Tigris recommended, or local MinIO) for encrypted backup with Restic.
See [GETTING-STARTED.md](GETTING-STARTED.md).

### Self-host bundle — `storage.yaml`

If you installed with the bundle, edit `/etc/vulos/storage.yaml`:

```yaml
# This box's own filesystem — the DEFAULT a new install writes. No provider.
backend: local
local:
  root: /var/lib/vulos/storage

# A hosted S3-compatible provider (R2 / B2 / Wasabi / AWS / Tigris)
backend: tigris                 # backend selector
endpoint: https://<your-provider-endpoint>
access_key: YOUR_ACCESS_KEY
secret_key: YOUR_SECRET_KEY
bucket: your-bucket-name
region: us-east-1               # match your provider/bucket region

# Or local MinIO (installed by --storage=minio)
backend: minio
endpoint: http://127.0.0.1:9000
access_key: vulos
secret_key: (read from /var/lib/vulos/minio/.minio_secret at start)
bucket: vulos
```

Full walkthrough (including MinIO/Garage supervision and hardening):
[SELF-HOST-BUNDLE.md → Storage backends](SELF-HOST-BUNDLE.md#storage-backends).

### Environment variables (advanced / container deploys)

For containerised or hand-rolled deployments, the box reads two independent S3
configurations directly from the environment:

- **Per-user object store (Files/Drive, app storage):** `VULOS_STORAGE_ENDPOINT`,
  `VULOS_STORAGE_REGION`, `VULOS_STORAGE_ACCESS_KEY`, `VULOS_STORAGE_SECRET_KEY`,
  `VULOS_STORAGE_BUCKET`, `VULOS_STORAGE_USE_SSL`.
- **Backup vault (Restic):** `S3_ENDPOINT`, `S3_BUCKET`, `S3_ACCESS_KEY`,
  `S3_SECRET_KEY`, `S3_REGION`, and the encryption passphrase
  `VULOS_RESTIC_PASSWORD` — **set a real secret before enabling backups in
  production; the box fails closed on the dev default.**

The full variable reference, including per-app STS isolation, is in
[CONFIGURATION.md → Storage](CONFIGURATION.md#storage-two-independent-s3-configs).
Backup and restore procedures live in [BACKUP-RECOVERY.md](BACKUP-RECOVERY.md).

---

**See also:** [SELF-HOST-BUNDLE.md](SELF-HOST-BUNDLE.md) ·
[CONFIGURATION.md](CONFIGURATION.md) · [FILES.md](FILES.md) ·
[BACKUP-RECOVERY.md](BACKUP-RECOVERY.md) · [GETTING-STARTED.md](GETTING-STARTED.md)
