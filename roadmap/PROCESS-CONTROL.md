# Process Control

Seeing what is running on the box, and ending it — including something that has
stopped responding.

> **Goal.** An Activity Monitor that can list processes, apps and connections,
> say honestly whether each is responding, and end one safely: a graceful stop
> first, force only if that is refused, and a report of which actually happened.
> **Non-goals.** A general remote-shell. A per-user process ownership model —
> processes on a Vulos box are box-level, not user-level. Pattern-matching
> process killers of any kind (see "Never by name", below).
> **Status.** ✅ Shipped. `internal/proctl` (identity, protect list, escalation,
> X11 liveness ping), `services/telemetry` (HTTP surface), and
> `frontend/src/builtin/activity` (the app). One **open question** on the
> bare-metal path is recorded at the bottom; it fails safe today.

---

## The shape of the problem

A pid is only a process while that process lives. Once it is reaped the kernel
may hand the number to something else, so a signal sent by number afterwards
lands on a stranger. Everything here follows from that.

`internal/procgroup` already solved this for **children** — processes the server
spawned and holds an `*exec.Cmd` for — with the guard `cmd.ProcessState == nil`
("Wait has not returned yet"). That guard is unavailable for an Activity
Monitor by construction: it lists pids out of `/proc`, there is no `exec.Cmd`,
nobody is waiting on them, and the pid it is asked to kill is a number a browser
read seconds ago and posted back. That is the stale-pid shape exactly.

## Identity

The kernel keeps one identity stable across a boot:

    (pid, starttime)

`starttime` is field 22 of `/proc/<pid>/stat` — the clock ticks since boot at
which *that* process began. A recycled pid is a different process and carries a
different starttime. Every entry point takes the starttime the caller observed
and re-reads `/proc` to confirm it still holds; a mismatch is refused, never
signalled, and answered **409** rather than 404, because 404 reads as "already
gone" and invites the retry that is precisely wrong.

The check is repeated on every poll of the escalation wait, including the one at
its deadline — the gap between SIGTERM and SIGKILL is by design a period in
which time passes, and that is exactly where a pid gets recycled.

### The client has to bind the pair too

The server's check catches a client that sends a **stale** pair. It cannot
catch one that sends a **fresh** pair for the wrong process, because on the wire
those requests are identical.

The Activity Monitor produced exactly that for a while: the selection was a bare
pid, re-resolved against each 3-second poll, so a recycled pid silently re-aimed
the selection at whatever inherited the number — same row, same highlight, only
the name text changing — and the confirmation then read pid *and* starttime off
that new row, where they agreed. Fixed by binding the pair at selection time
(`processKey`). Worth remembering when any other surface grows a kill button:
**a correct server check does not make a client safe.**

## What may never be signalled

`proctl.Protect` binds admins too. It is a deny list, deliberately: a box exists
to run the owner's software, and an allow list would have to enumerate every
program a user might install to be usable, which in practice means it gets
widened until it allows everything. The role gate makes the capability rare;
this list stops the rare capability being self-destructive.

| Rule | Why |
|---|---|
| `pid == 1` | On bare metal `cmd/init` is PID 1; "kill 1" is "power off the box from a web page". In a container it is the entrypoint. |
| the server itself | Kills the audit log's own writer, and reads to the user as the box crashing. |
| the server's process **group** | Covers the supervisor/session it was started from. |
| kernel threads | The kernel discards signals to them, so the button could not work. |

Deliberately **not** on the list: uid. On the netboot path the server runs as
root and so does everything else, so a "no root processes" rule would refuse
every process on exactly the deployment where the feature is most needed — the
shape of a rule that gets deleted rather than obeyed.

## Escalation

`quit` sends SIGTERM, waits out the grace period (default 5s, client-overridable
within `[0, MaxGrace]`), then SIGKILL if the process is still there. `force`
sends SIGKILL alone. The result names the signals actually sent, the outcome
(`already_gone` / `terminated` / `killed` / `survived`), the ending state and
the elapsed time.

`survived` is real and is not an error path: a task in uninterruptible sleep
(state `D`, almost always blocked storage or a wedged mount) cannot be killed
until the I/O returns, and a zombie is already dead. Reporting that honestly is
the point — claiming success would leave the user believing they had fixed a
stuck disk.

The UI reports the escalation rather than just the outcome, because "killed" on
its own omits the fact the user needs: the polite request was refused and
unsaved work was destroyed to carry it out.

## "Not responding" — what is actually measured

Five-valued, never a boolean, and every answer carries the **method** that
produced it so a measurement can be told from the absence of one.

| Status | Method | What it means |
|---|---|---|
| `responding` | `http_probe`, `x11_ping` | Something was asked and it answered. |
| `not_responding` | `http_probe`, `x11_ping` | Something was asked and it did not. |
| `display_not_responding` | `x11_ping` | The **display** stopped answering, so no question about the app could be put to it. |
| `unknown` | `none` | No mechanism exists to ask. Vulos does not know. |
| `not_applicable` | `client_side` | The question is a category error (a built-in React view). |

Two rules that are easy to get wrong and are enforced by tests:

- **A `/proc` state letter is never a verdict.** `D` looks the most like
  "frozen" and is usually not an application fault at all — it is a task blocked
  in a syscall, overwhelmingly storage. It gets a note, not a badge.
- **An HTTP 5xx is `responding`.** A 500 means the server accepted the
  connection, routed the request and generated a reply, which is the evidence
  that its loop is running. Treating it as frozen would invite a user to
  force-quit something that was about to log a stack trace.

`display_not_responding` is its own status rather than a flavour of
`not_responding` because it has a different remedy. When an X server stalls
every application on it goes silent at once; they are all fine and the display
is not. Folding it in would put a "this app is broken" badge on a healthy app,
and the user's obvious response — force-quit it — destroys work and does not
unfreeze the picture.

### Never by name

Every X window tool on the box (`xdotool`, `xprop`, `xwininfo`) asks the X
**server** about a window, and the server answers from its own memory whether or
not the client that owns it is alive. `xdotool search --name` succeeds against a
client wedged for an hour. That is a proxy signal wearing the clothes of a
measurement, and this repo has shipped that exact mistake before. `_NET_WM_PING`
is the only exchange that requires the *client* to act, so `proctl` speaks the
protocol itself and executes nothing. A gate enforces it.

The same reasoning bans `pkill` and every pattern-matching killer repo-wide: an
unanchored `pkill -9 sh` once matched **bash** and produced a CI exit-137 that
cost a day. Kill by exact pid after confirming identity.

## Authorisation

Ending a process is admin-only (`POST /api/system/processes/signal`,
`POST /api/proc/apps/close`); the listings are readable by any authenticated
session. None of it is in `auth`'s `publicPaths`, and SEC-HARD-08 enforces that.
The handler fails closed on a nil `IsAdmin`, honours the operator exec
kill-switch, and audits before signalling.

Admin rather than something narrower because there is no per-user process
ownership in Vulos to scope it to — a finer gate would have to be invented
rather than enforced, and an invented one is the kind that turns out not to be
checked anywhere.

The UI's own permission check is an **affordance, not a gate**: it decides what
to offer, and fails toward offering while the session is still loading. The
server is the boundary.

---

## OPEN: `self_group` on the bare-metal path

**Established by reading the tree; not yet checked on a booted box.**

`cmd/init` starts the server with `exec.Command` and no `Setpgid` — a grep for
`Setpgid`/`SysProcAttr` across `backend/cmd/init/*.go` returns nothing. A child
inherits its parent's process group, and PID 1's group is 1. So on netboot the
server's PGID is very likely **1**, and so is that of everything else `init`
spawns the same way (avahi, dbus, sshd, udhcpc, wpa_supplicant).

If that holds, `Protect`'s `self_group` rule — "shares a process group with the
Vulos server" — matches nearly every system service on that deployment, and the
Activity Monitor will refuse to end any of them.

**This fails safe.** It refuses kills; it never permits one. Two things keep it
from being a functional problem today:

- `pid == 1` is already denied by its own rule, so nothing depends on the group
  rule to protect init.
- The **primary use case still works.** Apps get their own process groups —
  `services/appnet/launcher_proc_{linux,other}.go` and every spawn in
  `services/stream/pool.go` set `Setpgid: true` — so an app the OS launched is
  not caught by `self_group` and remains killable.

Under Docker/systemd the question does not arise: the server is either the
container entrypoint (covered by `self` and `init`) or a systemd service in its
own group as leader, and it runs as `vulos`, so the kernel returns EPERM for
root-owned services regardless of what `Protect` says.

**Why it is not fixed here.** Narrowing the rule (e.g. skipping the group check
when `self.PGID == 1`, on the grounds that "group 1" is not a meaningful group)
would make init-spawned services killable on netboot. That may even be
desirable for a stuck avahi, but it also puts sshd and dbus in reach, and that
is a policy decision that should be taken against an observed box rather than
against a reading of the source.

**What a netboot check should settle:**

1. On a booted bare-metal box, read field 5 of `/proc/<server-pid>/stat` and
   confirm the server's PGID. Is it 1?
2. List which processes share it (`ps -eo pid,pgid,comm`).
3. Confirm an ordinary app launched through the OS is still endable from the
   Activity Monitor there — this is the case the feature exists for.
4. Only then decide whether `self_group` should be narrowed, and to what.

**Test gap this leaves.** `TestProtect_DeniesTheProcessesThatWouldBrickTheBox`
uses `self{PID:500, PGID:500, SID:500}` — the systemd shape. There is no case
for `self.PGID == 1`. Whatever (1) settles, a case for the netboot shape should
be added so the two deployments are both represented.

---

## How this area is verified

The guards here are privileged code, so reading them is not evidence. Each is
mutation-tested: break the thing the guard protects, confirm the suite goes
red, revert programmatically, confirm the tree is clean. The identity check,
every `Protect` rule, the escalation order, the admin gate, the kill-switch, the
frozen-detection categories and the client-side selection binding all have a
recorded red run.

**A note on the harness itself.** The first version reported every mutation as
survived. `go test ./... | tail -20` returns *`tail`'s* exit status, not the
test run's, so a red run read as green — the harness had become the hollow gate
it existed to catch. Two things fixed it and both are worth keeping:

- `bash -o pipefail -c` for any command with a pipe.
- **A no-op control.** The harness applies a harmless comment change and
  asserts it is reported as *surviving*. A mutation tool that cannot demonstrate
  both outcomes is only claiming to distinguish them. Do not remove this as
  scaffolding — it is the part that proves the rest of the runs mean anything.

Three separate agents hit the swallowed-exit-status shape in one night, in
`go test | tail`, in a build behind a pipe, and in a truncated `head`. Treat any
pipeline whose exit status you rely on as broken until it has been shown to fail.
