# lilmail, installed by the real installer, on both architectures

> **Date.** 2026-08-16. **Scope.** One question: does `registry.json`'s `lilmail`
> entry install and run through `appnet.InstallFromRegistry` — the function
> `POST /api/store/install` reaches — on **amd64** and on **arm64**?
>
> **Harness.** [`scripts/verify-app-install-perarch.sh`](../scripts/verify-app-install-perarch.sh),
> committed as part of this pass. It did not exist before: the per-arch runs
> behind [`PER-ARCH-ARTIFACTS.md`](PER-ARCH-ARTIFACTS.md) §5 lived only in an
> agent's transcript, and a verification nobody can re-run is a claim, not
> evidence. §1 below says why the harness that *was* committed
> (`verify-app-recipe.sh`) could not answer this question.
>
> Companion to [`PER-ARCH-ARTIFACTS.md`](PER-ARCH-ARTIFACTS.md) (the change) and
> [`FIRST-PARTY-REGISTRY-TRUTH.md`](FIRST-PARTY-REGISTRY-TRUTH.md) (the artefact
> facts).

---

## 0. Result

| arch | how it ran here | verdict | evidence |
| --- | --- | --- | --- |
| **arm64** | **native** (this Mac is arm64) | **PASS** | §3 |
| **amd64** | **emulated** (qemu, via Docker) | see §4 | §4 |

Which one was emulated is stated because it changes what the run proves about
*timing* and nothing else: an emulated run that serves is still an install that
worked, but an emulated run that times out is a statement about qemu, not about
the recipe. The two are never merged into one verdict, and no arm64 result is
allowed to stand in for an amd64 one.

## 1. Why the existing harness could not answer this

`scripts/verify-app-recipe.sh` is the catalogue sweep, and it is the right tool
for the 55 third-party entries. It cannot test the three first-party ones, for
three independent reasons:

1. **It only runs on the host's architecture.** A per-arch recipe has two
   answers; it can ask one. On this Mac that is arm64, forever.
2. **It verifies against the repo's real trust anchor**, so an entry carrying
   `signature: ""` — which diwan, wede and lilmail do deliberately, because the
   founder runs the ceremony — can never reach the install path at all. Finding
   out whether the recipe works *after* the ceremony is the wrong order.
3. **Its strongest runtime assertion is that the command execve's.** lilmail
   execve's happily while serving nothing, and it has already been observed
   serving *degraded* in a way only an HTTP request plus a log read could see.

So the harness here is new, and everything it does not need to re-derive it
reuses: the generated-driver-over-the-product's-own-installer shape, the
`--in-container` dispatch, the `docker commit` a base image once discipline, and
the ephemeral-key idea from that script's `--self-test` fixture.

## 2. What a PASS means here, part by part

**The real installer ran.** The container runs a generated driver whose install
step is `appnet.LoadRegistry` → `appnet.InstallFromRegistry`. No install logic is
re-implemented, so the signature check, ARCH-01, `validateRecipeSecurity`,
`ResolveArtifact`, the checksum comparison, `extractZip`, the manifest write and
POSTINSTALL-01 are all the shipped ones. A harness that drives its own copy of
the install logic tests the harness.

**Signature verification was ON, at full strength.** `VULOS_ENV` unset (which
`services/env` reads as prod), `VULOS_REGISTRY_INSECURE` never set,
`VULOS_SIGN_ALLOW_KEY_MISMATCH` never set — and the driver *refuses to run* if it
finds any of them set, rather than merely not setting them itself. Trust is
rooted in an Ed25519 root key generated **inside the container**, which issues a
real release cert via `signing.IssueReleaseCert` and signs the one entry under
test with `appnet.SignEntry`. `VULOS_TRUST_ANCHOR` / `VULOS_RELEASE_CERT` point
at that material. It lives for the length of the run and dies with the
container.

This proves **the recipe works**. It deliberately proves nothing about who
vetted the entry: `registry.json` is copied, never written, and still ships
`signature: ""` for all three first-party entries. Conflating "the recipe
installs" with "the publisher vetted it" is how a signature stops meaning
anything.

**The right artefact, checksum-matched.** `facts.json` records the URL and digest
`ResolveArtifact` returned for the box's own arch, and the assertion compares
them against what `staticInstall` logged after hashing the bytes. An absent
`checksum OK` line is a failure, not a shrug — "no comparison happened" is
exactly the state that looks like a pass.

**Extracted, not copied.** Two assertions, because the pre-`extractZip` bug
(install `bin/<name>.zip`, `chmod 0755`, report success) satisfies either alone:
no `*.zip` anywhere under the app dir, **and** `file` reporting an ELF of the
machine type this box actually is. The second is also what would catch a
resolver handing over the *other* architecture's artefact — its checksum would
match perfectly and the app would simply never run.

**It serves.** `GET /login` must be 200 **and** carry lilmail's own `<title>`.
The launch mirrors `appnet.Launcher` in every part that can change the verdict:
cwd is the manifest's `work_dir`, `sh -c` on the manifest command with `${PORT}`
expanded, a scrubbed env of exactly `PATH`/`HOME`/`TMPDIR`/`PORT`, and `setpriv
--reuid=65534 --regid=65534 --clear-groups --no-new-privs`. Dropping to 65534 is
not ceremony: `post_install` writes `config.toml` mode 0640 owned by 65534, so a
run as root would hide a permissions defect every real box would hit.

**Not faithful, stated plainly:** no `ip netns exec` (it needs CAP_SYS_ADMIN and
an iproute2 stack this image does not carry) and no run-lease. Neither can make
a non-serving app serve, and `scripts/prove-launch.sh` is where the namespace
path is proved on a real kernel.

**And it did not silently degrade.** The app's own log is read, and any
`unavailable` / `store open failed` line fails the run even when `/login`
returned 200.

## 3. arm64 — PASS, native

```
· trust-root            ephemeral ed25519 root+release issued in-container; VULOS_ENV unset
· box-arch              installer resolved for arm64 (driver runtime.GOARCH)
· artifact              …/v1.14.0/lilmail_1.14.0_linux_arm64.zip
✓ install               InstallFromRegistry returned OK in 1s
✓ checksum-verified     matched the registry pin 7689dd62a46c… for arm64
✓ manifest-written      /var/lib/vulos/apps/lilmail/app.json
✓ command-declared      ./lilmail
✓ extracted-not-copied  no .zip left under the app dir
✓ binary-executable     /var/lib/vulos/apps/lilmail/lilmail
✓ arch-correct          arm64 ⇐ ELF 64-bit LSB executable, ARM aarch64, statically linked, stripped
✓ post-install-config   config.toml written (640 nobody:nogroup)
✓ serves-login          GET /login → 200 after 2s
✓ serves-own-page       <title>lilmail</title>
· serves-root           GET / → 302
✓ not-degraded          no degradation reported in the app's own log
· dir-cache             present (755 nobody:nogroup)
· dir-sessions          present (755 nobody:nogroup)
══ PASS lilmail on linux/arm64 (native) ══
```

The installer's own log for that run is the corroboration that the **zip branch**
ran rather than the tar branch or the plain-binary branch:

```
[registry] trust anchor /out/prep/trust-anchor.pub → release key "perarch-verify-ephemeral"
[registry/static] downloading …/lilmail_1.14.0_linux_arm64.zip
[registry/static] checksum OK (7689dd62a46c)
[registry/static] extracted zip into /var/lib/vulos/apps/lilmail (strip=1, 2 members)
[registry] installed lilmail@1.14.0 → /var/lib/vulos/apps/lilmail
```

and the app's own log shows Fiber bound on 8090 and answering:

```
20:57:02 | 200 |  695.86µs | 127.0.0.1 | GET | /login | -
20:57:02 | 302 |   57.54µs | 127.0.0.1 | GET | /      | -
```

## 4. amd64 — emulated

**RUN IN PROGRESS at the time of this commit.** The emulated amd64 run is under
way; its result — pass, fail, or did-not-finish — is landed in the very next
commit to this file, whatever it says. It is deliberately not inferred from the
arm64 result, and this section is deliberately not left implying one.

## 5. Whose job is `cache/`

**Answer: the recipe's, and nothing else's.**

- The **installer** creates `bin/`, `static/` and `data/` — the strict bundle
  structure `AppManifest` documents — and nothing app-specific. That is now
  pinned by `TestInstallFromRegistry_CreatesOnlyTheBundleDirs`, which goes red if
  anyone adds `cache/` to that loop, so the choice has to be made deliberately
  for all 56 entries rather than discovered when a per-recipe `mkdir` quietly
  becomes dead code.
- The **app** does not create it. Without the directory lilmail logs
  `scheduled send unavailable (store open failed)` and runs **degraded** while
  still answering `GET /login` with 200 — which is precisely why a status-code
  check alone is not enough.
- The **recipe** does, in `post_install`, alongside `sessions/`.

`TestShippedLilmailPostInstall_CreatesTheDirsItsConfigNames` **runs** the shipped
`post_install` rather than pattern-matching it. Running it is the point: the last
defect in this field was a quoting one — `\'` inside a single-quoted `sh` string
ends the quote instead of escaping — which no substring check would have caught
and which shipped an app whose every launch died with `Failed to load config`. It
then asserts the outcomes: both directories exist, `config.toml`'s
`[cache].folder` names the directory that was actually created, `[jwt].secret`
and `[encryption].key` are 32 characters and *different*, and a second install
produces different secrets. `chown` is stubbed to a no-op (no unprivileged test
can chown to 65534) and that single line is asserted textually, which the test
says out loud rather than quietly pretending otherwise.

Verified red against three mutants: the installer creating `cache/`; the recipe
dropping its `cache` mkdir; the recipe reusing one secret for both fields.

## 6. The tar-independence guard does kill

`TestStaticInstall_ZipDoesNotDependOnTar` exists because a mutation routing zip
to the `tar` branch survived the whole suite: macOS ships BSD tar (libarchive),
which extracts zips happily, while the shipped Debian image ships GNU tar, which
cannot read one at all. A guard whose own kill has not been seen is not a guard,
so all three mutations were run — via `go test -overlay`, so `registry.go` was
never modified in this shared checkout.

| mutant | what it models | verdict |
| --- | --- | --- |
| `isZipURL(url)` → `false && isZipURL(url)` at the dispatch site | zip detection disabled | **killed** |
| `isZipURL` body → `return false` | zip detection disabled at the source | **killed** |
| `.zip` added back to `tarExtensions` **and** the zip dispatch disabled | **the historical fix-that-hid-itself** | **killed** |

The third is the one that matters, and it is the one the test was written for.
Under it, on this Mac, `TestStaticInstall_ZipIsExtractedNotDroppedAsAFile`
**passes** — bsdtar quietly does the job — and across the entire `appnet`
package the only new red is `TestStaticInstall_ZipDoesNotDependOnTar`. (The two
other reds in that run are the pre-existing, deliberate
`…ShippedAnchorVerifiesShippedRegistry` / `…HoldsNoUnsignedEntry` failures over
`signature: ""`, which are the ceremony reminder working as designed and must not
be silenced.) So the guard is not merely present — it is the *sole* thing
standing between this repo and a defect that is invisible here and fatal on every
box a user owns.

The first two mutants are killed for a slightly different reason worth recording:
with zip detection off, a `.zip` no longer matches `isTarURL` either, so it falls
to the **plain-binary** branch and reproduces the original bug exactly
(`bin/lilmail_1.14.0_linux_amd64.zip`, `chmod 0755`, install reports success).
The stub `tar` is never even invoked. That is a real kill of a real defect, but
it is not a test of tar-independence; only the third mutant exercises that, and
only the guard catches it.

## 7. Re-running this

```sh
# native here
bash scripts/verify-app-install-perarch.sh lilmail --arch arm64

# emulated here; native on a Linux/amd64 host
bash scripts/verify-app-install-perarch.sh lilmail --arch amd64

# both, native first
bash scripts/verify-app-install-perarch.sh lilmail --arch both

# the harness's own reds: corrupted checksum and unsigned entry must both FAIL
bash scripts/verify-app-install-perarch.sh lilmail --arch arm64 --self-test

# the control that makes "not-degraded" mean something
bash scripts/verify-app-install-perarch.sh lilmail --arch arm64 --control cachedir
```

It works for any entry, not just lilmail — `diwan` and `wede` are the obvious
next two, and unlike the transcript-only runs behind `PER-ARCH-ARTIFACTS.md` §5,
anyone can now re-run them.

**Not recorded in `roadmap/app-verification-ledger.json`.** That ledger's row
schema carries one `arch` per app and is written by `verify-app-recipe.sh`; a
per-arch result cannot be expressed in it without either overwriting the other
architecture's row or inventing a second meaning for the same field. This note is
the record until that schema grows a per-arch shape.

## 8. Corrections to sibling notes

`PER-ARCH-ARTIFACTS.md` §9 lists "**`post_install` failure is only a warning**"
as the highest-value remaining installer fix. That is **no longer true** —
POSTINSTALL-01 landed since (`registry.go`: a failed `post_install` is fatal and
the half-built app directory is removed, with
`TestInstallFromRegistry_FailedPostInstallIsFatal` pinning it). The rest of that
§9 list still holds. The file is not edited here because it is another agent's.

Agent-generated, machine-verified, NOT human-reviewed.
