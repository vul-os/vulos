package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"vulos/backend/services/reach"
	"vulos/backend/services/reach/rendezvous"
	"vulos/backend/services/reach/tunnel"
)

// relay.go — `vulos relay`, the RELAY ROLE of this same binary.
//
// A relay is not a separate product and not a separate download. Any Vulos
// install that happens to have a public IP can serve as the public half of
// other boxes' tunnels by running this subcommand. That is deliberate: making
// a relay a role rather than a product means standing one up introduces no new
// vendor, no new supply chain, and no new operator relationship — it is
// software you already run and already trust.
//
//	vulos relay serve      run a relay
//	vulos relay grant      mint a grant entry to paste into the grants file

const relayUsage = `vulos relay — run the public half of a Vulos reverse tunnel

Usage:
  vulos relay serve [flags]              run a relay
  vulos relay grant <name> [name...]     print a grant entry with a fresh token

Serve flags:
  -addr           listen address (default :8443, or VULOS_RELAY_ADDR)
  -domain         base domain, e.g. relay.example.com (required, or VULOS_RELAY_DOMAIN)
  -grants-file    path to the JSON grants file (or VULOS_RELAY_GRANTS inline)
  -revoked-file   path to the JSON revocation list (optional)
  -cert -key      terminate TLS in-process; omit both to run plain HTTP behind a proxy
  -path-mode      also serve /t/<name>/ when you have no wildcard DNS
  -trust-proxy-headers
                  believe X-Forwarded-For — ONLY behind a trusted TLS-terminating proxy
  -admin-addr     loopback admin/status listener (default 127.0.0.1:9090; empty disables)
  -admin-token    bearer token required for a NON-loopback admin bind
  -max-agents     concurrent tunnel cap (default 64)
  -node-id        stable identifier for this relay, shown to agents
  -direct-probe   let agents advertise a verified direct endpoint
  -rendezvous     also serve the discovery role, so boxes in different houses
                  can find each other (mounted on the apex host only)
  -grace          graceful-shutdown budget after SIGTERM (default 20s)

Environment twins exist for the secret-bearing flags so credentials never
appear in ps output: VULOS_RELAY_GRANTS, VULOS_RELAY_ADMIN_TOKEN.
`

func runRelay(args []string) error {
	if len(args) == 0 {
		return usagef("%s", relayUsage)
	}
	switch args[0] {
	case "serve":
		return runRelayServe(args[1:])
	case "grant":
		return runRelayGrant(args[1:])
	case "-h", "--help", "help":
		fmt.Print(relayUsage)
		return nil
	default:
		return usagef("unknown relay command %q\n\n%s", args[0], relayUsage)
	}
}

// runRelayGrant mints a grant entry. Generating the token here rather than
// telling the operator to "pick a strong secret" removes the single most
// common way a deployment ends up weak.
func runRelayGrant(args []string) error {
	if len(args) == 0 {
		return usagef("usage: vulos relay grant <name> [name...]")
	}
	for _, n := range args {
		if err := tunnel.ValidName(n); err != nil {
			return usagef("%v", err)
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("generating token: %w", err)
	}
	entry := tunnel.Grant{Token: hex.EncodeToString(buf), Names: args}
	out, err := json.MarshalIndent([]tunnel.Grant{entry}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", out)
	fmt.Fprintf(os.Stderr, `
Add the entry above to the relay's grants file (chmod 600), and give the same
token to the box as its relay endpoint token. The relay never displays a token
again after startup, so save it now.
`)
	return nil
}

func runRelayServe(args []string) error {
	fs := flag.NewFlagSet("vulos relay serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		addr        = fs.String("addr", envOr("VULOS_RELAY_ADDR", ":8443"), "listen address")
		domain      = fs.String("domain", envOr("VULOS_RELAY_DOMAIN", ""), "base relay domain")
		grantsFile  = fs.String("grants-file", envOr("VULOS_RELAY_GRANTS_FILE", ""), "JSON grants file")
		revokedFile = fs.String("revoked-file", envOr("VULOS_RELAY_REVOKED_FILE", ""), "JSON revocation list")
		certFile    = fs.String("cert", "", "TLS certificate file")
		keyFile     = fs.String("key", "", "TLS key file")
		pathMode    = fs.Bool("path-mode", envOr("VULOS_RELAY_PATH_MODE", "") == "1", "also serve /t/<name>/")
		trustProxy  = fs.Bool("trust-proxy-headers", envOr("VULOS_RELAY_TRUST_PROXY_HEADERS", "") == "1",
			"believe X-Forwarded-For (only behind a trusted proxy)")
		adminAddr   = fs.String("admin-addr", envOr("VULOS_RELAY_ADMIN_ADDR", "127.0.0.1:9090"), "admin listener")
		adminToken  = fs.String("admin-token", envOr("VULOS_RELAY_ADMIN_TOKEN", ""), "bearer for a non-loopback admin bind")
		maxAgents   = fs.Int("max-agents", 0, "concurrent tunnel cap (0 = default)")
		nodeID      = fs.String("node-id", envOr("VULOS_RELAY_NODE_ID", ""), "stable relay identifier")
		directProbe = fs.Bool("direct-probe", envOr("VULOS_RELAY_DIRECT_PROBE", "") == "1",
			"let agents advertise a verified direct endpoint")
		allowPrivDirect = fs.Bool("direct-allow-private", false, "allow direct endpoints on private addresses")
		enableRdv       = fs.Bool("rendezvous", envOr("VULOS_RELAY_RENDEZVOUS", "") == "1",
			"also serve the announce/resolve discovery role")
		rdvPrefix = fs.String("rendezvous-prefix", envOr("VULOS_RELAY_RENDEZVOUS_PREFIX", rendezvous.DefaultPathPrefix),
			"mount prefix for the discovery role")
		rdvOrigins = fs.String("rendezvous-origins", envOr("VULOS_RELAY_RENDEZVOUS_ORIGINS", ""),
			"comma-separated browser origins allowed to call the discovery role (empty = any)")
		grace = fs.Duration("grace", 20*time.Second, "graceful-shutdown budget")
	)
	if err := fs.Parse(args); err != nil {
		return usagef("%v", err)
	}

	if strings.TrimSpace(*domain) == "" {
		return usagef("-domain is required (e.g. -domain relay.example.com)")
	}
	// FAIL CLOSED on a half-specified TLS config. A relay that silently fell
	// back to plaintext because one of the two flags was missing would serve
	// every tunnelled session in the clear while its operator believed
	// otherwise — the worst kind of failure, because nothing looks wrong.
	if (*certFile == "") != (*keyFile == "") {
		return usagef("-cert and -key must be given together")
	}

	grants, err := loadRelayGrants(*grantsFile)
	if err != nil {
		return err
	}
	rev, err := tunnel.LoadRevocationsFile(*revokedFile)
	if err != nil {
		return err
	}
	store, err := tunnel.NewGrantStore(grants, rev)
	if err != nil {
		return err
	}

	// The discovery role is OFF by default: a plain reverse-tunnel relay is a
	// complete, useful thing, and every role an operator did not ask for is
	// surface they did not choose to expose.
	var rdvHandler http.Handler
	var rdvSvc *rendezvous.Service
	if *enableRdv {
		rdvSvc = rendezvous.New(rendezvous.Config{
			PathPrefix:     *rdvPrefix,
			AllowedOrigins: reach.SplitList(*rdvOrigins),
			Logf:           log.Printf,
		})
		rdvHandler = rdvSvc.Handler()
	}

	srv, err := tunnel.NewServer(tunnel.ServerConfig{
		Domain:             *domain,
		PathMode:           *pathMode,
		Grants:             store,
		MaxAgents:          *maxAgents,
		TrustProxyHeaders:  *trustProxy,
		NodeID:             *nodeID,
		EnableDirectProbe:  *directProbe,
		AllowPrivateDirect: *allowPrivDirect,
		RendezvousHandler:  rdvHandler,
		RendezvousPrefix:   *rdvPrefix,
		Logf:               log.Printf,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv.Handler(),
		// ReadHeaderTimeout bounds a slowloris that never finishes its
		// headers. There is deliberately no ReadTimeout or WriteTimeout: a
		// tunnelled request may legitimately be a long upload, a download, or
		// a WebSocket that stays open for hours, and a blanket timeout would
		// sever those.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	if rdvSvc != nil {
		log.Printf("[relay] discovery role enabled at https://%s%s (apex host only)", *domain, rdvSvc.Prefix())
		log.Printf("[relay] the discovery role stores and echoes announced endpoints VERBATIM and never " +
			"dials them, so it serves boxes on a private mesh (Tailscale/Headscale/Nebula) with no " +
			"extra configuration here. The address screening is on the BOX, which must grant the " +
			"mesh range with VULOS_PEER_ALLOW_CIDR before it will dial a peer the relay named.")
	}

	// The relay makes exactly ONE outbound request on an agent's behalf — the
	// direct-endpoint ownership probe — so that is the whole of its dial grant,
	// and it says so rather than leaving an operator to infer it. A grant
	// nobody can see is a grant nobody audits.
	switch {
	case !*directProbe:
		log.Printf("[relay] dial grant: NONE — the direct-endpoint probe is off (-direct-probe), " +
			"so this relay makes no outbound request on any agent's behalf")
	case *allowPrivDirect:
		log.Printf("[relay] dial grant: -direct-allow-private is ON — the ownership probe may dial " +
			"PRIVATE and CGNAT addresses (10/8, 172.16/12, 192.168/16, 100.64/10, ULA). Correct for a " +
			"relay whose agents are reachable only on a private mesh; on a public relay it lets any " +
			"granted agent aim one probe at this host's own network. Loopback, link-local " +
			"(169.254.169.254) and bogons stay refused.")
	default:
		log.Printf("[relay] dial grant: the ownership probe may dial PUBLIC addresses only; private, " +
			"CGNAT, loopback, link-local and bogon endpoints are refused. A relay serving agents on a " +
			"private mesh needs -direct-allow-private, which is a coarse grant — read its startup note.")
	}

	adminSrv, err := startRelayAdmin(*adminAddr, *adminToken, srv)
	if err != nil {
		return err
	}

	// Signal handling: SIGTERM drains first (stop accepting NEW tunnels,
	// keep serving existing ones) and only then shuts down, so a rolling
	// restart moves boxes to another relay instead of dropping them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		var err error
		if *certFile != "" {
			log.Printf("[relay] listening on %s (TLS in-process) for *.%s", *addr, *domain)
			err = httpSrv.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			log.Printf("[relay] listening on %s (plain HTTP — terminate TLS at your edge) for *.%s", *addr, *domain)
			err = httpSrv.ListenAndServe()
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	if !*trustProxy && *certFile == "" {
		log.Printf("[relay] NOTE: running plain HTTP without -trust-proxy-headers. " +
			"If a TLS-terminating proxy fronts this relay, pass -trust-proxy-headers so client IPs are correct; " +
			"if this listener is directly internet-facing, leave it off (clients could otherwise spoof their IP) " +
			"and give it -cert/-key instead.")
	}

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Printf("[relay] shutdown signal received; draining for up to %s", *grace)
	srv.Drain()
	shutCtx, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	if adminSrv != nil {
		_ = adminSrv.Shutdown(shutCtx)
	}
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Printf("[relay] shutdown: %v", err)
	}
	return nil
}

// loadRelayGrants resolves grants from a file or the inline environment,
// preferring the file. A relay with neither refuses to start — see grants.go
// for why "never runs open" is a hard rule rather than a default.
func loadRelayGrants(path string) ([]tunnel.Grant, error) {
	if strings.TrimSpace(path) != "" {
		return tunnel.LoadGrantsFile(path)
	}
	if inline := strings.TrimSpace(os.Getenv("VULOS_RELAY_GRANTS")); inline != "" {
		return tunnel.ParseGrants(inline)
	}
	return nil, errors.New(
		"no grants configured: pass -grants-file or set VULOS_RELAY_GRANTS.\n" +
			"Mint one with:  vulos relay grant <box-name>")
}

// startRelayAdmin brings up the status listener.
//
// The roster it serves NAMES EVERY BOX registered here, so it is a genuine
// disclosure surface, not just diagnostics. Two rules follow: it binds
// loopback by default, and a non-loopback bind REQUIRES a token rather than
// merely warning. An operator who binds it to 0.0.0.0 to reach it from their
// laptop should get a startup error, not a public list of everyone's boxes.
func startRelayAdmin(addr, token string, srv *tunnel.Server) (*http.Server, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, nil
	}
	loopback, err := isLoopbackBind(addr)
	if err != nil {
		return nil, fmt.Errorf("-admin-addr %q: %w", addr, err)
	}
	if !loopback && strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf(
			"-admin-addr %q is not loopback and no -admin-token was set: the admin listener "+
				"reveals every registered box, so a routable bind requires a token", addr)
	}

	mux := http.NewServeMux()
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if token != "" {
				got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	// tls-ask answers an automatic-TLS terminator's "should I issue a
	// certificate for this hostname?" question (Caddy's on_demand_tls `ask`,
	// and equivalents). It is served ONLY here, on the loopback admin
	// listener, because a public version would let anyone enumerate which box
	// names are provisioned.
	//
	// It is intentionally NOT behind the admin token: a TLS terminator on the
	// same host needs it on every new hostname, and requiring a shared secret
	// between two processes on one loopback interface adds a moving part
	// without adding a boundary. If the admin listener is bound off-loopback,
	// the token requirement above already gates the whole listener.
	mux.HandleFunc("GET /tls-ask", func(w http.ResponseWriter, r *http.Request) {
		if srv.ServesHostname(r.URL.Query().Get("domain")) {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not a name this relay serves", http.StatusNotFound)
	})
	mux.HandleFunc("GET /tunnels", authed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(srv.Tunnels())
	}))

	s := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[relay] admin listener: %v", err)
		}
	}()
	log.Printf("[relay] admin listener on %s", addr)
	return s, nil
}

// isLoopbackBind reports whether addr binds only the loopback interface. An
// empty or wildcard host ("", "0.0.0.0", "::") is NOT loopback — that is the
// case the token requirement exists for.
func isLoopbackBind(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "" {
		return false, nil // ":9090" binds every interface
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	return strings.EqualFold(host, "localhost"), nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
