package main

// lan_rootcert.go — ROOTDIST-01: hand the LAN CA root certificate to a browser.
//
// D101 built everything except the one step a human performs. A padlock on
// https://vulos.local requires the owner to install a root certificate on each
// device, and until now there was no way to GET that file onto a device from
// the box: the CA runs off-box, the box held only the leaf it serves, and the
// owner's only route was to carry a file off the operator machine by hand to
// every phone in the house. internal/lan/lanroot.go gives the box a copy; these
// two routes hand it out.
//
//	GET /api/lan/rootcert           → JSON: is there a root, and what is it
//	GET /api/lan/rootcert/download  → the PEM, as a download
//
// # The first fetch is NOT authenticated, and the UI must not pretend it is
//
// The owner downloads the root over TLS their device does not yet trust — that
// is the definition of the situation, and it cannot be engineered away by the
// box. A network attacker on the LAN could substitute their own root here.
//
// What makes that acceptable is the same thing that makes `-print-pairing`
// acceptable: the FINGERPRINT is verifiable out of band. The JSON route returns
// the SHA-256 of the certificate DER, the Settings panel shows it before the
// download button, and both tell the owner to compare it against
// `vulos-lanca inspect` on the machine that holds the CA. A substituted root
// cannot match that value. The panel says the first fetch is unverified in as
// many words; nothing here claims otherwise.
//
// # Why both routes are session-gated
//
// Same reason as /api/lan/pairing (see lan_pairing.go): auth's publicPaths is
// an exhaustive allow-list enforced by services/security_test.go's SEC-HARD-08,
// so gating is the DEFAULT for any new route and staying out of that list is
// the decision. The root certificate itself carries no secret, but these routes
// disclose the box's name, its LAN address, and the human label the owner chose
// for their CA, and there is no reason an unauthenticated stranger on the LAN
// should enumerate them.
//
// The practical cost is that a phone must be signed in to the box before it can
// follow the QR code to the download. That is one sign-in the owner was going
// to do anyway, and the panel says so rather than leaving them at a raw 401.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"vulos/backend/internal/lan"
)

// rootCertDownloadPath is the download route, in one place so the JSON payload
// and the handler registration cannot drift.
const rootCertDownloadPath = "/api/lan/rootcert/download"

// rootCertFileName is what the browser saves the file as. `.crt` (not `.pem`)
// because that is the extension Windows, macOS and Android all associate with a
// certificate install flow; a `.pem` lands in a text editor on several of them.
const rootCertFileName = "vulos-root.crt"

// rootCertInfo is the wire shape of GET /api/lan/rootcert.
//
// `present` is a first-class field rather than an inference from a 404 because
// "this box has no CA root" is a NORMAL state, not a fault: the CA is operated
// off-box and an owner may never have run it. A surface that renders a missing
// root as an error teaches the owner something is broken when nothing is.
type rootCertInfo struct {
	Present bool `json:"present"`

	// Problem is set when a file IS at the root path but was refused — not a
	// CA, unconstrained, unparsable. It is the owner-facing reason, verbatim
	// from internal/lan, because a refusal the owner cannot read is a refusal
	// they will work around.
	Problem string `json:"problem,omitempty"`

	Subject string `json:"subject,omitempty"`

	NotBefore string `json:"not_before,omitempty"`
	NotAfter  string `json:"not_after,omitempty"`
	Expired   bool   `json:"expired,omitempty"`

	// SHA256 is THE value to compare out of band. SHA1 is included only for
	// certificate dialogs that show nothing else (see lan.RootInfo).
	SHA256 string `json:"sha256,omitempty"`
	SHA1   string `json:"sha1,omitempty"`

	PermittedDNS []string `json:"permitted_dns,omitempty"`
	PermittedIP  []string `json:"permitted_ip,omitempty"`
	PathLenZero  bool     `json:"path_len_zero,omitempty"`

	// DownloadPath is same-origin, for the in-page button. DownloadURL is
	// absolute against the box's LAN IP, for the QR code a phone scans —
	// deliberately the IP and not a .local name, because a phone that cannot
	// resolve mDNS is the case this whole flow exists for (Chrome on Android;
	// see lanServiceRef.certIPs).
	DownloadPath string `json:"download_path"`
	DownloadURL  string `json:"download_url,omitempty"`
}

// lanRootCertPath resolves where the root certificate lives, overridable for
// tests and for operators who keep box state somewhere else — matching how
// lanPairingCertSource resolves the leaf paths.
func lanRootCertPath() string {
	if v := os.Getenv("VULOS_LAN_ROOT_CERT"); v != "" {
		return v
	}
	return lan.DefaultRootPath
}

// buildRootCertInfo reads and vets the root at path and renders it for the UI.
// It never returns an error: every failure is a state the owner needs described,
// not a 500.
func buildRootCertInfo(path, downloadURL string) rootCertInfo {
	out := rootCertInfo{DownloadPath: rootCertDownloadPath}

	info, err := lan.LoadRootInfo(path)
	switch {
	case errors.Is(err, lan.ErrRootNotPresent):
		return out
	case err != nil:
		out.Problem = err.Error()
		return out
	}

	out.Present = true
	out.Subject = info.Subject
	out.NotBefore = info.NotBefore.UTC().Format(time.RFC3339)
	out.NotAfter = info.NotAfter.UTC().Format(time.RFC3339)
	out.Expired = info.Expired(time.Now())
	out.SHA256 = info.SHA256Hex
	out.SHA1 = info.SHA1Hex
	out.PermittedDNS = info.PermittedDNS
	out.PermittedIP = info.PermittedIP
	out.PathLenZero = info.PathLenZero
	out.DownloadURL = downloadURL
	return out
}

// rootCertDownloadURL builds the absolute URL a phone should open. Uses the
// box's detected LAN IP and the LAN HTTPS listener's port, exactly like
// PairingAddr does, so the QR points at an address a device with no mDNS can
// actually dial.
func rootCertDownloadURL() string {
	addr := lan.PairingAddr(lanPairingHTTPSAddr())
	if addr == "" {
		return ""
	}
	return fmt.Sprintf("https://%s%s", addr, rootCertDownloadPath)
}

// registerLANRootCertRoutes mounts both routes on mux.
//
// Deliberately NOT added to auth's publicPaths — see the file header.
func registerLANRootCertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/lan/rootcert", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildRootCertInfo(lanRootCertPath(), rootCertDownloadURL()))
	})

	mux.HandleFunc("GET "+rootCertDownloadPath, func(w http.ResponseWriter, r *http.Request) {
		// Re-vetted on every download rather than trusted from the status call.
		// The manual install path drops a file at this location by hand and
		// never passes through the puller's checks, so the ONLY place that can
		// stop an unconstrained CA reaching an owner's trust store is here.
		info, err := lan.LoadRootInfo(lanRootCertPath())
		if errors.Is(err, lan.ErrRootNotPresent) {
			writeErr(w, http.StatusNotFound, "This box has no LAN CA root certificate. "+
				"Run `vulos-lanca init` on your own computer, then have it deliver the root to this box "+
				"(or copy `vulos-lanca root` output to "+lanRootCertPath()+").")
			return
		}
		if err != nil {
			// 409, not 500: the box is working correctly and is refusing on
			// purpose. A 500 would read as "try again", and the owner would.
			writeErr(w, http.StatusConflict, err.Error())
			return
		}

		// application/x-x509-ca-cert is the type Android and iOS associate with
		// a CA install flow; served with an attachment disposition so a desktop
		// browser saves the file instead of rendering base64 as text.
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		w.Header().Set("Content-Disposition", `attachment; filename="`+rootCertFileName+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The fingerprint travels with the bytes, so a user who downloaded via
		// curl or a QR scan can compare without going back to the JSON route.
		w.Header().Set("X-Vulos-Root-SHA256", info.SHA256Hex)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(info.PEM)
	})
}
