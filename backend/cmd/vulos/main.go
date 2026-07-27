// vulos — the local command-line client for your own Vulos box.
//
// This binary talks ONLY to the box whose URL you configure (VULOS_BOX_URL,
// default http://localhost:8080). It never sends your credentials anywhere
// else — every request, and the bearer token it carries, goes to that one
// configured host (see client.go).
//
// # Command surface
//
//	vulos web deploy <dir> [--site NAME]      Package <dir> as a tar.gz and
//	                                          upload it as a static site.
//	vulos web list                            List your hosted sites.
//	vulos web rm <site>                       Delete a site.
//	vulos web domain add <site> <domain>      Attach a custom domain; prints the
//	                                          exact DNS records to create and
//	                                          polls until the domain goes active.
//	vulos web domain status <site> <domain>   Show a domain's activation status.
//	vulos web domain rm <site> <domain>       Detach a custom domain.
//
//	vulos relay serve [flags]                 Run the RELAY ROLE: the public half
//	                                          of a Vulos reverse tunnel, so boxes
//	                                          with no public IP are reachable.
//	vulos relay grant <name> [name...]        Mint a grant entry with a fresh
//	                                          random token.
//
// The relay subcommand is the one part of this binary that does NOT talk to a
// box: it IS a server. See docs/REACH.md.
//
// # Authentication
//
// The box authenticates every /api/web route from the X-User-ID header its auth
// middleware stamps after validating the caller's token. This CLI presents that
// token as `Authorization: Bearer <VULOS_TOKEN>`. The box accepts three bearer
// shapes on that header — a regular session token, an admin token
// (`vulos-admin-…`), or a CP-issued API key (`vk_…`) — so VULOS_TOKEN may be any
// of them. Bearer (rather than a session cookie) is used deliberately: it keeps
// the CSRF branch in the middleware from engaging on the binary upload.
//
// # Exit codes
//
//	0  success
//	1  usage error
//	2  operation error
package main

import (
	"fmt"
	"os"
)

const usage = `vulos — local CLI for your Vulos box

Usage:
  vulos web deploy <dir> [--site NAME]      package <dir> and upload it as a site
  vulos web list                            list your hosted sites
  vulos web rm <site>                       delete a site
  vulos web domain add <site> <domain>      attach a custom domain (prints DNS, polls)
  vulos web domain status <site> <domain>   check a domain's activation status
  vulos web domain rm <site> <domain>       detach a custom domain

  vulos relay serve [flags]                 run the public half of a reverse tunnel
  vulos relay grant <name> [name...]        mint a relay grant with a fresh token

Environment:
  VULOS_BOX_URL   box base URL (default http://localhost:8080)
  VULOS_TOKEN     bearer token for your box session (required for all commands)
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch args[0] {
	case "web":
		if err := runWeb(args[1:]); err != nil {
			if _, isUsage := err.(usageError); isUsage {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	case "relay":
		if err := runRelay(args[1:]); err != nil {
			if _, isUsage := err.(usageError); isUsage {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(2)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		os.Exit(1)
	}
}

// usageError marks an error as a caller mistake (bad arguments) so main can exit
// with code 1 instead of 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) usageError {
	return usageError{msg: fmt.Sprintf(format, a...)}
}
