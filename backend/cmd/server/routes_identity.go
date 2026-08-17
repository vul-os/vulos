package main

import (
	"encoding/json"
	"net/http"
	"os"
	"runtime"

	"vulos/backend/internal/lan"
	"vulos/backend/services/identity"
)

// routes_identity.go — the box's own identity: its instance ULID and the name
// its owner chose for it.
//
// WHAT WAS BROKEN (found 2026-08-17). The install wizard's "name this box"
// field posted here, and this handler validated the name, persisted it, and
// best-effort wrote /etc/hostname. It then did nothing else, which made
// choosing a name actively worse than not having the feature:
//
//  1. It NEVER called sethostname. The running system kept the old name and
//     kept advertising it over mDNS, so study.local did not start working —
//     and the response said success. A silent no-op reporting success is the
//     failure mode this project keeps finding.
//  2. It NEVER touched the certificate. SANs were a hard-coded
//     {"vulos.local", lanHost}, so the moment the new name DID take effect
//     (next reboot) the box answered to a name its certificate never
//     mentioned: a permanent name-mismatch warning. The naming feature, once
//     it worked, broke TLS.
//  3. There was no way to find out the name was already taken by a sibling
//     box. avahi resolves that conflict silently, hours later, by renaming the
//     loser to vulos-2 — a name nothing knows about.
//
// All three are fixed here. The rename is applied live (system hostname + mDNS
// re-advertisement), the certificate follows because its SANs are derived from
// the same lan.NameSet rather than declared, and a name can be checked for
// availability BEFORE it is committed.

// hostnameRenamer is the box-renaming capability this file needs, kept as an
// interface so the handlers are testable without a live LAN service. It is
// implemented by *lanServiceRef (lan_pairing.go).
type hostnameRenamer interface {
	// rename applies a new box name live and returns the resulting name set.
	rename(name string) (lan.NameSet, error)
	// nameTaken reports whether another box on this link already answers to
	// name, and which address answered.
	nameTaken(name string) (bool, string)
	// names returns the box's current name set.
	names() lan.NameSet
}

// hostnameResponse is what a rename or an availability check reports back.
//
// It reports what ACTUALLY took effect, never just what was asked for.
// AppliedLive is the field the UI must read: when it is false the new name is
// persisted but the running system still answers to the old one, and the user
// has to be told that rather than shown a success tick.
type hostnameResponse struct {
	Instance    *identity.Instance `json:"instance,omitempty"`
	Hostname    string             `json:"hostname"`
	Names       []string           `json:"names"`               // .local names the box now answers to
	AppliedLive bool               `json:"applied_live"`        // false => a restart is needed
	Notice      string             `json:"notice,omitempty"`    // human-readable caveat when AppliedLive is false
	Available   *bool              `json:"available,omitempty"` // availability check only
	TakenBy     string             `json:"taken_by,omitempty"`  // availability check only
}

// identityResponse is GET /api/identity's body.
//
// DefaultHostname is the per-instance name the box would use if nobody chose
// one (lan.DefaultHostname — "vulos-<6 ULID chars>"). The wizard PREFILLS the
// name field with it, and that prefill is load-bearing rather than cosmetic:
// while every box defaulted to the bare "vulos", an owner who simply clicked
// Next ended up on the same name as every sibling box, and two boxes on one
// LAN then answered the same mDNS query — measured as a coin flip, with TLS
// succeeding on the wrong box because both certificates carried that name.
// Showing a unique name by default is what stops the collision being the
// default outcome.
type identityResponse struct {
	*identity.Instance
	DefaultHostname string   `json:"default_hostname"`
	Names           []string `json:"names,omitempty"`
}

// registerIdentityRoutes wires the identity API into mux.
//
//	GET  /api/identity                    — returns the current Instance as JSON
//	GET  /api/identity/hostname/available — is this name free on the LAN?
//	POST /api/identity/hostname           — validates, applies live, and persists
func registerIdentityRoutes(mux *http.ServeMux, home string, renamer hostnameRenamer) {
	// Load (or generate) the instance at startup; cache it in a closure-scoped pointer
	// that is refreshed on each hostname write so reads are always consistent.
	inst, err := identity.Load(home)
	if err != nil {
		// Non-fatal: log and serve a zero-value instance so the server still starts.
		inst = &identity.Instance{}
	}

	mux.HandleFunc("GET /api/identity", func(w http.ResponseWriter, r *http.Request) {
		// The Instance's own fields stay at the TOP LEVEL (embedded, untagged),
		// because the wizard reads data.ulid and data.hostname directly and a
		// wrapper would silently break both. The derived fields are added
		// alongside.
		resp := identityResponse{Instance: inst}
		if renamer != nil {
			ns := renamer.names()
			resp.DefaultHostname = ns.Unique
			resp.Names = ns.MDNS
		} else {
			resp.DefaultHostname = lan.DefaultHostname(inst.ULID)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// AVAILABILITY CHECK — the good answer to the two-box collision.
	//
	// A second box on the LAN is detected while the owner is TYPING the name,
	// not silently renamed by avahi hours later. This is the same mDNS conflict
	// probe the advertiser runs before claiming a name, exposed to the UI so it
	// can behave like a username field.
	//
	// Note the box's OWN current name reads as available: re-submitting the
	// name you already have must not look like a conflict with yourself.
	mux.HandleFunc("GET /api/identity/hostname/available", func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("name")
		name := lan.SanitizeHostname(raw)
		if name == "" {
			writeErr(w, 422, "hostname must be lowercase alphanumeric with hyphens (max 63 chars)")
			return
		}

		available := true
		takenBy := ""
		if renamer != nil && name != renamer.names().Hostname {
			if taken, by := renamer.nameTaken(name); taken {
				available, takenBy = false, by
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hostnameResponse{
			Hostname:  name,
			Available: &available,
			TakenBy:   takenBy,
		})
	})

	mux.HandleFunc("POST /api/identity/hostname", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hostname string `json:"hostname"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}

		// One definition of what a box name IS, shared with the mDNS
		// advertisement and the certificate SANs (internal/lan/names.go).
		// A local regexp here would be a fourth copy of the same rule.
		name := lan.SanitizeHostname(req.Hostname)
		if name == "" {
			writeErr(w, 422, "hostname must be lowercase alphanumeric with hyphens (max 63 chars)")
			return
		}

		inst.Hostname = name
		if err := identity.Save(inst, home); err != nil {
			writeErr(w, 500, "failed to persist identity: "+err.Error())
			return
		}

		// Persist for the next boot. The trailing newline is CORRECT —
		// hostname(5) specifies a newline-terminated file. The bug was never
		// this writer; it was cmd/init reading the file and handing the raw
		// bytes to sethostname(2), which made the box's hostname literally
		// "vulos\n" (measured 2026-08-17, see cmd/init/parseEtcHostname).
		if runtime.GOOS == "linux" {
			if info, err := os.Stat("/etc/hostname"); err == nil && !info.IsDir() {
				_ = os.WriteFile("/etc/hostname", []byte(name+"\n"), info.Mode())
			}
		}

		resp := hostnameResponse{Instance: inst, Hostname: name}

		// Apply LIVE. Both halves have to work for the rename to be real:
		// the kernel's hostname (which avahi-daemon and everything reading
		// os.Hostname() follow) and our own mDNS advertisement.
		liveErr := setSystemHostname(name)

		if renamer != nil {
			ns, err := renamer.rename(name)
			if err != nil {
				writeErr(w, 422, err.Error())
				return
			}
			resp.Names = ns.MDNS
			resp.AppliedLive = liveErr == nil
		} else {
			// No LAN service (VULOS_LAN_ENABLE unset): nothing advertises the
			// name, so the rename cannot take effect on the network at all.
			resp.Names = lan.NewNameSet(inst.ULID, name).MDNS
			resp.AppliedLive = false
		}

		if !resp.AppliedLive {
			resp.Notice = "The name is saved, but this box is still answering to its previous name on the network. Restart the box to finish the rename."
			if liveErr != nil {
				resp.Notice += " (" + liveErr.Error() + ")"
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}
