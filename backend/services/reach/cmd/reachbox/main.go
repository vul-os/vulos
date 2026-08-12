// Command reachbox is a minimal stand-in for a Vulos BOX, for local relay
// testing only. It is not shipped and not installed by any build or install
// script — grep build.sh and scripts/install-vulos.sh, neither mentions it.
//
// # Why this exists
//
// The real box process is backend/cmd/server, which brings up the entire OS
// (storage, every service, hardware checks, a data directory) before it ever
// gets to reachwire.go's three lines of tunnel wiring. That is far too heavy
// to spin up twice, on two ports, as "boxes" in a repeatable local harness —
// and cmd/server is out of scope for this change regardless.
//
// reachbox does exactly what reachwire.go does and nothing else: it loads the
// relay endpoint set with reach.FromEnv() (the SAME box-side config surface
// production uses — same env vars, same file-permission rule, same
// one-bad-entry-refuses-the-whole-set rule) and, if the set is non-empty,
// starts a tunnel.Agent against it. It exists so scripts/smoke-relay.sh can
// run genuinely separate OS processes for "two boxes" and "two relays"
// against the real `vulos relay serve` binary, without touching cmd/server.
//
// # Config
//
//	VULOS_RELAY_ENDPOINTS_FILE / VULOS_RELAY_ENDPOINTS / the legacy three —
//	  exactly as documented in docs/REACH.md and implemented by reach.FromEnv.
//	REACHBOX_LABEL   — identifies this box in its own HTTP responses and logs.
//	                   Defaults to "reachbox".
//	REACHBOX_LISTEN  — OPTIONAL host:port for a plain HTTP listener serving the
//	                   SAME handler the tunnel serves. Unset (the default) means
//	                   no listener at all, which is what scripts/smoke-relay.sh
//	                   uses.
//
//	                   It exists for ONE purpose: scripts/smoke-relay-nat.sh
//	                   needs a direct-dial target in order to PROVE that the
//	                   relay cannot reach the box directly. Without something
//	                   listening, "the relay could not connect to the box" is
//	                   true because nothing was listening, not because the box
//	                   is behind NAT — the control probe would pass for the
//	                   wrong reason and the NAT claim would be unfounded. With
//	                   it, the same probe succeeds the moment the box is NOT
//	                   isolated, which is what makes the isolation testable.
//
// # Failure posture
//
// Mirrors reachwire.go: a configuration error (bad file permissions, one bad
// endpoint, a malformed URL) is logged loudly and this process keeps running
// with ZERO tunnels — never exits non-zero, never panics. That is the
// production posture, and it is also what lets a smoke script assert "the box
// process stayed up but published nothing" rather than conflating a config
// refusal with a crash.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"vulos/backend/services/reach"
	"vulos/backend/services/reach/tunnel"
)

func main() {
	log.SetFlags(0)
	label := strings.TrimSpace(os.Getenv("REACHBOX_LABEL"))
	if label == "" {
		label = "reachbox"
	}

	set, src, err := reach.FromEnv()
	if err != nil {
		// LOUD and NON-FATAL, exactly like reachwire.go: a box with a broken
		// relay config still boots, reachable on every OTHER path it has. The
		// prefix is a stable grep anchor for the smoke script.
		log.Printf("[reachbox %s] CONFIG REFUSED: %v", label, err)
		log.Printf("[reachbox %s] endpoints: 0 (config refused — see above)", label)
	} else {
		log.Printf("[reachbox %s] endpoints: %d (source: %s)", label, set.Len(), src)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if set.Len() == 0 {
		log.Printf("[reachbox %s] no tunnels to hold — idling until signalled", label)
		<-ctx.Done()
		log.Printf("[reachbox %s] exiting", label)
		return
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "reachbox:%s path=%s", label, r.URL.Path)
	})

	// The optional direct listener. Same handler, so a response proves nothing
	// about WHICH path served it — the caller distinguishes them by where it
	// connected, which is the whole point of the NAT control probe.
	if addr := strings.TrimSpace(os.Getenv("REACHBOX_LISTEN")); addr != "" {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			// Loud and fatal: this listener exists to make a NEGATIVE claim
			// testable ("the relay cannot reach this"). If it silently failed
			// to bind, the probe that must fail would fail for the wrong
			// reason and the harness would report a NAT property it never
			// established.
			log.Fatalf("[reachbox %s] REACHBOX_LISTEN=%s: %v", label, addr, err)
		}
		log.Printf("[reachbox %s] direct listener on %s", label, ln.Addr())
		srv := &http.Server{Handler: handler}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()
	}

	targets := make([]tunnel.Target, 0, set.Len())
	for _, ep := range set.All() {
		targets = append(targets, tunnel.Target{
			WSURL: ep.WSURL(),
			Name:  ep.Name,
			Token: ep.Token,
			Label: ep.BaseURL,
		})
	}

	agent, err := tunnel.NewAgent(tunnel.AgentConfig{
		Targets:   targets,
		Handler:   handler,
		UserAgent: "reachbox/dev",
		Logf:      func(f string, a ...any) { log.Printf("[reachbox %s] "+f, append([]any{label}, a...)...) },
		OnState: func(st tunnel.LinkStatus) {
			log.Printf("[reachbox %s] link %s -> %s (name=%s err=%q)", label, st.Label, st.State, st.Name, st.LastError)
		},
	})
	if err != nil {
		log.Printf("[reachbox %s] tunnel agent NOT started: %v", label, err)
		<-ctx.Done()
		return
	}

	agent.Start(ctx)
	<-ctx.Done()
	log.Printf("[reachbox %s] shutting down", label)
	_ = agent.Close()
}
