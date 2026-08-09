package wsutil

import (
	"net/http"
	"testing"
)

// withPrivateOrigins sets the developer relaxation for the duration of a test
// and restores it afterwards.
func withPrivateOrigins(t *testing.T, allow bool) {
	t.Helper()
	prev := AllowPrivateOrigins()
	SetAllowPrivateOrigins(allow)
	t.Cleanup(func() { SetAllowPrivateOrigins(prev) })
}

func req(host, origin string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://"+host+"/api/telemetry", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

// check drives the EXACT field the shipped services use
// (wsutil.Upgrader.CheckOrigin), not a local copy of the function.
func check(r *http.Request) bool {
	if Upgrader.CheckOrigin == nil {
		panic("Upgrader.CheckOrigin is nil — gorilla would then allow ALL origins")
	}
	return Upgrader.CheckOrigin(r)
}

// TestCheckOrigin_DefaultIsStrict pins the fail-closed default: a package that
// never calls SetAllowPrivateOrigins must reject every cross-origin handshake.
func TestCheckOrigin_DefaultIsStrict(t *testing.T) {
	if AllowPrivateOrigins() {
		t.Fatal("package default must be strict (allowPrivateOrigins=false)")
	}
	for _, origin := range []string{
		"http://localhost:5173",
		"http://127.0.0.1:8080",
		"http://192.168.1.9",
		"https://evil.example",
	} {
		if check(req("box.example.com", origin)) {
			t.Errorf("origin %q accepted with the strict default; want rejected", origin)
		}
	}
}

// TestCheckOrigin_PrefixSpoofRejected is the regression that matters: the old
// implementation matched the origin host with strings.HasPrefix against
// "localhost", "10.", "172.", "192.168." and "127.0.0.1", so any attacker who
// can register a hostname beginning with one of those strings could hijack a
// logged-in user's WebSocket. These must be rejected EVEN WITH the developer
// relaxation switched on.
func TestCheckOrigin_PrefixSpoofRejected(t *testing.T) {
	withPrivateOrigins(t, true)

	spoofs := []string{
		"https://localhost.attacker.example",
		"http://localhost-attacker.example",
		"https://127.0.0.1.attacker.example",
		"https://10.attacker.example",
		"https://172.attacker.example",
		"https://192.168.attacker.example",
		"https://0.0.0.0.attacker.example",
		"https://attacker.example",
		"null",
		"http://box.example.com.attacker.example",
	}
	for _, origin := range spoofs {
		if check(req("box.example.com", origin)) {
			t.Errorf("spoofed origin %q was ACCEPTED — cross-site WebSocket hijack", origin)
		}
	}
}

// TestCheckOrigin_SameOriginAndNoOrigin keeps the legitimate paths working.
func TestCheckOrigin_SameOriginAndNoOrigin(t *testing.T) {
	withPrivateOrigins(t, false)

	ok := []struct{ host, origin string }{
		{"box.example.com", "https://box.example.com"},
		{"box.example.com", "http://box.example.com"},
		{"box.example.com:8443", "https://box.example.com:8443"},
		{"BOX.example.com", "https://box.example.com"},
		{"192.168.1.9:8080", "http://192.168.1.9:8080"},
		{"box.example.com", ""}, // non-browser client
	}
	for _, c := range ok {
		if !check(req(c.host, c.origin)) {
			t.Errorf("host=%q origin=%q rejected; want accepted", c.host, c.origin)
		}
	}
}

// TestCheckOrigin_DevRelaxationAcceptsRealLiterals proves the relaxation still
// does its job for the Vite dev server, and only for real IP literals.
func TestCheckOrigin_DevRelaxationAcceptsRealLiterals(t *testing.T) {
	withPrivateOrigins(t, true)

	for _, origin := range []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://[::1]:5173",
		"http://192.168.1.9:5173",
		"http://10.0.0.4:5173",
		"http://172.16.0.4:5173",
	} {
		if !check(req("box.example.com", origin)) {
			t.Errorf("dev origin %q rejected with the relaxation on; want accepted", origin)
		}
	}

	// 172.32.x is OUTSIDE the RFC1918 172.16.0.0/12 block — the old "172."
	// prefix test called it private.
	if check(req("box.example.com", "http://172.32.0.4:5173")) {
		t.Error("public 172.32.0.4 accepted as a private origin")
	}
}
