package appnet

import "fmt"

// ISOLATION-PRIV-01 — the privilege posture of a process app, in production code.
//
// # Why these are functions and not literals at the call site
//
// They were literals inside launchWithConcurrency, and launcher_privilege_test.go
// asserted against test-local copies of them named buildAppCommand and
// buildAppEnv, each introduced with a comment saying it "mirrors" the real one.
// It did mirror it — that is the problem. Deleting setpriv, --reuid, --regid,
// --clear-groups and --no-new-privs from the launcher left all eight
// ISOLATION-PRIV-01 tests green, because not one of them ever read the code
// that launches anything. The suite reported the privilege drop as covered
// while covering a copy of it.
//
// One definition, used by the launcher and asserted by the tests, is the only
// arrangement in which those assertions mean what their names say.

// appCommandArgv is the exact argv used to start a process app.
//
// The chain is: ip netns exec (enters the network namespace, needs CAP_SYS_ADMIN
// and therefore still root) → setpriv (drops to appUID/appGID) → sh -c <command>.
//
// The drop cannot be expressed as SysProcAttr.Credential on this cmd: the direct
// child is `ip`, not the app, and stripping ip's privileges makes setns() fail
// so the namespace is never entered. setpriv runs one hop later — after namespace
// entry, before the application — which is the only point where both properties
// hold at once.
//
// Flag meanings, since each is a separate security property and each has its own
// regression test:
//
//	--reuid/--regid   set real AND effective uid/gid, so there is no saved-set
//	                  uid to return to.
//	--clear-groups    drop supplementary groups inherited from the server; without
//	                  it the app keeps every group root belonged to.
//	--no-new-privs    set PR_SET_NO_NEW_PRIVS, so a setuid binary inside the
//	                  namespace cannot raise privileges back up.
func appCommandArgv(nsName, expandedCmd string) []string {
	return []string{
		"ip", "netns", "exec", nsName,
		"setpriv",
		fmt.Sprintf("--reuid=%d", appUID),
		fmt.Sprintf("--regid=%d", appGID),
		"--clear-groups",
		"--no-new-privs",
		"sh", "-c", expandedCmd,
	}
}

// appProcessEnv is the scrubbed environment handed to a process app.
//
// It is built from a fixed allowlist and never from os.Environ(): the server
// holds API keys, tokens and relay credentials in its own environment, and an
// app process is not trusted with them.
//
// HOME is /tmp rather than /root because the app runs as appUID (nobody), which
// cannot write to /root — an app that assumes a writable HOME fails at startup
// otherwise. The two facts are one decision, which is why they live together.
//
// Caller env goes in after the base so an app can be configured, and PORT goes
// in last so the manifest's declared port cannot be overridden by caller env.
func appProcessEnv(extraEnv []string, appPort int) []string {
	env := append([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
		"HOME=/tmp",
		"TMPDIR=/tmp",
	}, extraEnv...)
	return append(env, fmt.Sprintf("PORT=%d", appPort))
}
