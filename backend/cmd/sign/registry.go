// registry.go — `vulos-sign sign-registry` / `verify-registry`.
//
// The App Hub registry (registry.json) is the third artifact class in the Vulos
// signing chain, alongside OS images and stable.json manifests:
//
//	offline ROOT key ──(issue-release-cert)──▶ release cert ──▶ RELEASE key
//	                                                              │
//	                        ┌─────────────────────────────────────┤
//	                        ▼                     ▼               ▼
//	                  os-core.squashfs.sig   stable.json.sig   registry.json
//	                                                           (per-entry sigs)
//
// sign-registry is a RELEASE-key operation: it runs on the maintainer's signing
// machine, never in CI.  verify-registry is the public half — it needs only the
// anchor and the cert, so CI runs it on every push to prove that every entry the
// repo ships is signed by the key the shipped anchor vouches for.
//
// See docs/KEY-CEREMONY.md.
package main

//lint:file-ignore ST1005 The release-refusal error is a multi-line operator
// banner, not a wrapped error string: it ends in a runnable command example
// (make sign-registry RELEASE_PRIV=...), and reflowing it to satisfy the style
// rule would cost the reader the instructions that make the refusal actionable.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"vulos/backend/services/appnet"
	"vulos/backend/services/signing"
)

// encodeB64 renders a public key in the same base64 form as trust-anchor.pub
// (without the trailing newline signing.EncodeAnchor adds for the file itself).
func encodeB64(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// ─── export-anchor ────────────────────────────────────────────────────────────

// cmdExportAnchor converts a public-key JSON file (hex) into the trust-anchor
// wire format (single-line base64) that signing.LoadAnchor reads and that
// scripts/seed/embed-anchor.sh bakes into the image.
//
// It exists so the ceremony needs nothing but this binary — no openssl, no jq,
// no hand-rolled base64 pipeline that could silently emit the wrong 32 bytes.
func cmdExportAnchor(args []string) {
	fs := flag.NewFlagSet("export-anchor", flag.ExitOnError)
	pubPath := fs.String("pub", "", "path to the ROOT public-key JSON file")
	outPath := fs.String("out", "trust-anchor.pub", "output path for the base64 anchor file")
	_ = fs.Parse(args)

	if *pubPath == "" {
		fatalf(1, "export-anchor: -pub is required")
	}
	pub, err := readPublicKey(*pubPath)
	if err != nil {
		fatalf(2, "export-anchor: %v", err)
	}
	mustWriteFile(*outPath, []byte(signing.EncodeAnchor(pub)), 0644)

	// Prove the file we just wrote parses back to the same key — an anchor that
	// LoadAnchor rejects would halt boot on every device that receives it.
	got, err := signing.LoadAnchor(*outPath)
	if err != nil {
		fatalf(2, "export-anchor: wrote %s but it does not parse: %v", *outPath, err)
	}
	if !got.Equal(ed25519.PublicKey(pub)) {
		fatalf(2, "export-anchor: round-trip mismatch writing %s", *outPath)
	}

	fmt.Printf("export-anchor: wrote %s\n", *outPath)
	fmt.Printf("  pubkey (base64): %s\n", encodeB64(pub))
}

// ─── sign-registry ────────────────────────────────────────────────────────────

func cmdSignRegistry(args []string) {
	fs := flag.NewFlagSet("sign-registry", flag.ExitOnError)
	releasePrivPath := fs.String("release-priv", "", "path to the release private-key JSON file")
	registryPath := fs.String("registry", "registry.json", "path to registry.json (rewritten in place)")
	_ = fs.Parse(args)

	if *releasePrivPath == "" {
		fatalf(1, "sign-registry: -release-priv is required")
	}

	privBytes, err := readPrivateKey(*releasePrivPath)
	if err != nil {
		fatalf(2, "sign-registry: read release private key: %v", err)
	}
	releasePriv := ed25519.PrivateKey(privBytes)

	reg, err := appnet.LoadRegistry(*registryPath)
	if err != nil {
		fatalf(2, "sign-registry: load %s: %v", *registryPath, err)
	}
	if len(reg.Apps) == 0 {
		fatalf(2, "sign-registry: %s contains no apps — refusing to write an empty registry", *registryPath)
	}

	for _, appID := range sortedAppIDs(reg) {
		if err := appnet.SignEntry(reg.Apps[appID], appID, releasePriv); err != nil {
			fatalf(2, "sign-registry: sign entry %q: %v", appID, err)
		}
	}

	if err := appnet.SaveRegistry(*registryPath, reg); err != nil {
		fatalf(2, "sign-registry: write %s: %v", *registryPath, err)
	}

	// Signing is worthless if we cannot prove the result verifies. Re-read from
	// disk and check every signature against the public half of the key we just
	// signed with — so a serialisation bug can never ship a registry that the
	// box will reject at install time.
	releasePub, ok := releasePriv.Public().(ed25519.PublicKey)
	if !ok {
		fatalf(2, "sign-registry: release key is not Ed25519")
	}
	n, err := verifyRegistryFile(*registryPath, releasePub)
	if err != nil {
		fatalf(2, "sign-registry: post-write verification FAILED: %v", err)
	}

	fmt.Printf("sign-registry: signed and verified %d entries in %s\n", n, *registryPath)
	fmt.Printf("  release pubkey (base64): %s\n", encodeB64(releasePub))
}

// ─── verify-registry ──────────────────────────────────────────────────────────

func cmdVerifyRegistry(args []string) {
	fs := flag.NewFlagSet("verify-registry", flag.ExitOnError)
	anchorPath := fs.String("anchor", signing.DefaultAnchorPath, "path to the trust-anchor public key (root)")
	certPath := fs.String("cert", "", "path to the root-signed release cert (omit for the single-key model)")
	registryPath := fs.String("registry", "registry.json", "path to registry.json")
	unverifiedPath := fs.String("unverified", appnet.UnverifiedRegistryFile,
		"path to the unverified quarantine registry to cross-check against (may not exist)")
	requireProd := fs.Bool("require-prod-keys", false,
		"fail if the trust material is the well-known DEV keypair (use in release builds)")
	_ = fs.Parse(args)

	anchor, err := signing.LoadAnchor(*anchorPath)
	if err != nil {
		fatalf(2, "verify-registry: %v", err)
	}

	// The key entries must be signed by: the release key if a cert chains it to
	// the anchor, otherwise the anchor itself.
	verifyKey := anchor
	keyDesc := fmt.Sprintf("trust anchor %s", *anchorPath)
	if *certPath != "" {
		cert, err := signing.LoadReleaseCert(*certPath)
		if err != nil {
			fatalf(2, "verify-registry: %v", err)
		}
		releasePub, err := signing.ReleaseKeyFromCert(anchor, cert)
		if err != nil {
			fatalf(2, "verify-registry: release cert %s does not chain to anchor %s: %v",
				*certPath, *anchorPath, err)
		}
		verifyKey = releasePub
		keyDesc = fmt.Sprintf("release key %q (cert %s, expires %s)", cert.KeyID, *certPath, cert.NotAfter)
	}

	n, err := verifyRegistryFile(*registryPath, verifyKey)
	if err != nil {
		fatalf(2, "verify-registry: %v", err)
	}

	fmt.Printf("verify-registry: OK — all %d entries in %s are signed by %s\n", n, *registryPath, keyDesc)

	// COVERAGE ASSERTION. "All N entries verified" is worth nothing if N is not
	// every entry the file contains. Re-read the raw JSON and count the app IDs
	// the parser saw, so a duplicate key (which the map silently collapses,
	// hiding the shadowed entry from verification) or a dropped entry is a hard
	// failure rather than a smaller, still-green N.
	if err := assertVerifiedEveryEntry(*registryPath, n); err != nil {
		fatalf(2, "verify-registry: %v", err)
	}
	fmt.Printf("verify-registry: coverage — %d/%d app IDs in %s were verified (0 skipped)\n",
		n, n, *registryPath)

	// QUARANTINE CROSS-CHECK. registry.json is the signed set and has no
	// exception path; entries that are not ready to be signed live in the
	// unverified quarantine registry instead. Prove the two files stay disjoint
	// and that nothing in the quarantine is dressed up to look signed.
	if err := crossCheckQuarantine(*unverifiedPath, *registryPath); err != nil {
		fatalf(2, "verify-registry: %v", err)
	}

	if err := releaseKeyGate(verifyKey, *requireProd, *registryPath); err != nil {
		fatalf(2, "verify-registry: %v", err)
	}
	if signing.IsDevKey(verifyKey) {
		fmt.Fprintf(os.Stderr,
			"verify-registry: NOTE: this is the DEVELOPMENT key. A production box (VULOS_ENV=prod)\n"+
				"  will refuse it. Run the ceremony in docs/KEY-CEREMONY.md before shipping.\n")
	}
}

// releaseKeyGate refuses a dev-signed registry when the caller demands
// production keys (-require-prod-keys, used by the release workflow).
//
// A dev-signed registry verifies perfectly well — that is the point of the
// checked-in dev keypair. It just must never be what we SHIP: the dev private key
// is derived from a published seed, so anyone can sign an app for it. The runtime
// already refuses these keys in prod, which means a dev-signed image would boot
// with the App Hub dead. This gate stops us cutting that image at all, the same
// way netboot halts without os-core.roothash.sig: no founder signature, no release.
func releaseKeyGate(verifyKey ed25519.PublicKey, requireProd bool, registryPath string) error {
	if !requireProd || !signing.IsDevKey(verifyKey) {
		return nil
	}
	return fmt.Errorf("REFUSING TO RELEASE — %s is signed by the DEVELOPMENT key, whose\n"+
		"  private half is derived from a published seed (anyone can forge app signatures with it).\n"+
		"  A production box will refuse this registry and ship with the App Hub disabled.\n"+
		"  Run the offline key ceremony (docs/KEY-CEREMONY.md), install the real trust anchor at\n"+
		"  keys/trust-anchor.pub, and re-sign with: make sign-registry RELEASE_PRIV=...",
		registryPath)
}

// ─── publish-feed ─────────────────────────────────────────────────────────────
//
// Registry-as-feed, phase 1 (see backend/services/appnet/feed.go and
// roadmap/APP-STORE.md's "Forward plan: registry-as-feed" note). This is
// PURELY ADDITIVE: it appends a signed entry to registry-feed.json recording
// "the release key published this exact registry.json at this time", chained
// to every prior publication. It does not change what sign-registry does to
// registry.json, and no install-time verification path consults the feed yet.

func cmdPublishFeed(args []string) {
	fs := flag.NewFlagSet("publish-feed", flag.ExitOnError)
	releasePrivPath := fs.String("release-priv", "", "path to the release private-key JSON file")
	registryPath := fs.String("registry", "registry.json", "path to registry.json (read-only — the feed records its hash)")
	feedPath := fs.String("feed", "registry-feed.json", "path to registry-feed.json (appended to in place)")
	_ = fs.Parse(args)

	if *releasePrivPath == "" {
		fatalf(1, "publish-feed: -release-priv is required")
	}

	privBytes, err := readPrivateKey(*releasePrivPath)
	if err != nil {
		fatalf(2, "publish-feed: read release private key: %v", err)
	}
	releasePriv := ed25519.PrivateKey(privBytes)
	releasePub, ok := releasePriv.Public().(ed25519.PublicKey)
	if !ok {
		fatalf(2, "publish-feed: release key is not Ed25519")
	}

	before, err := appnet.LoadFeed(*feedPath)
	if err != nil {
		fatalf(2, "publish-feed: load %s: %v", *feedPath, err)
	}
	beforeN := len(before.Entries)

	feed, err := appnet.AppendFeedEntry(*feedPath, *registryPath, releasePub, releasePriv)
	if err != nil {
		fatalf(2, "publish-feed: %v", err)
	}

	if len(feed.Entries) == beforeN {
		fmt.Printf("publish-feed: no-op — %s is unchanged since the last publish (tip seq=%d, %s)\n",
			*registryPath, feed.Head.Seq, feed.Head.Tip)
		return
	}

	// Publishing is worthless if we cannot prove the result verifies — re-read
	// from disk and check the chain + signature, same discipline as
	// sign-registry's post-write verification.
	reloaded, err := appnet.LoadFeed(*feedPath)
	if err != nil {
		fatalf(2, "publish-feed: reload %s: %v", *feedPath, err)
	}
	if err := appnet.VerifyFeed(reloaded, releasePub); err != nil {
		fatalf(2, "publish-feed: post-write verification FAILED: %v", err)
	}

	fmt.Printf("publish-feed: appended seq=%d to %s (%d total entries)\n", feed.Head.Seq, *feedPath, len(feed.Entries))
	fmt.Printf("  registry_hash: %s\n", feed.Entries[len(feed.Entries)-1].RegistryHash)
	fmt.Printf("  tip:           %s\n", feed.Head.Tip)
	fmt.Printf("  signer (base64): %s\n", encodeB64(releasePub))
}

// ─── verify-feed ───────────────────────────────────────────────────────────────

func cmdVerifyFeed(args []string) {
	fs := flag.NewFlagSet("verify-feed", flag.ExitOnError)
	anchorPath := fs.String("anchor", signing.DefaultAnchorPath, "path to the trust-anchor public key (root)")
	certPath := fs.String("cert", "", "path to the root-signed release cert (omit for the single-key model)")
	feedPath := fs.String("feed", "registry-feed.json", "path to registry-feed.json")
	_ = fs.Parse(args)

	anchor, err := signing.LoadAnchor(*anchorPath)
	if err != nil {
		fatalf(2, "verify-feed: %v", err)
	}

	verifyKey := anchor
	keyDesc := fmt.Sprintf("trust anchor %s", *anchorPath)
	if *certPath != "" {
		cert, err := signing.LoadReleaseCert(*certPath)
		if err != nil {
			fatalf(2, "verify-feed: %v", err)
		}
		releasePub, err := signing.ReleaseKeyFromCert(anchor, cert)
		if err != nil {
			fatalf(2, "verify-feed: release cert %s does not chain to anchor %s: %v",
				*certPath, *anchorPath, err)
		}
		verifyKey = releasePub
		keyDesc = fmt.Sprintf("release key %q (cert %s, expires %s)", cert.KeyID, *certPath, cert.NotAfter)
	}

	feed, err := appnet.LoadFeed(*feedPath)
	if err != nil {
		fatalf(2, "verify-feed: load %s: %v", *feedPath, err)
	}
	if len(feed.Entries) == 0 {
		fatalf(2, "verify-feed: %s has never been published (no entries)", *feedPath)
	}
	if err := appnet.VerifyFeed(feed, verifyKey); err != nil {
		fatalf(2, "verify-feed: %v", err)
	}

	fmt.Printf("verify-feed: OK — %s (%d entries, tip seq=%d) verifies against %s\n",
		*feedPath, len(feed.Entries), feed.Head.Seq, keyDesc)
}

// ─── shared ───────────────────────────────────────────────────────────────────

// verifyRegistryFile loads registryPath and verifies every entry against pub.
// It returns the number of entries verified, and fails on the FIRST bad entry —
// an unsigned or invalid entry is a hard stop, never a warning.
func verifyRegistryFile(registryPath string, pub ed25519.PublicKey) (int, error) {
	reg, err := appnet.LoadRegistry(registryPath)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", registryPath, err)
	}
	if len(reg.Apps) == 0 {
		return 0, fmt.Errorf("%s contains no apps", registryPath)
	}
	for _, appID := range sortedAppIDs(reg) {
		if err := appnet.VerifyEntrySignature(reg.Apps[appID], appID, pub); err != nil {
			return 0, fmt.Errorf("entry %q: %w", appID, err)
		}
	}
	return len(reg.Apps), nil
}

// rawAppIDs returns the app IDs exactly as they appear in the file's "apps"
// object, in file order and INCLUDING duplicates.
//
// It exists to be a second, independent opinion about what is in the registry.
// LoadRegistry unmarshals into a map, which silently keeps only the last of any
// duplicated key — so a registry containing "foo" twice would verify one "foo"
// and never look at the other. Token-scanning the raw bytes sees both.
func rawAppIDs(registryPath string) ([]string, error) {
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", registryPath, err)
	}
	apps, ok := top["apps"]
	if !ok {
		return nil, fmt.Errorf(`%s has no top-level "apps" object`, registryPath)
	}

	dec := json.NewDecoder(bytes.NewReader(apps))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf(`parse %s "apps": %w`, registryPath, err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf(`%s "apps" is not a JSON object`, registryPath)
	}
	var ids []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf(`parse %s "apps": %w`, registryPath, err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf(`%s "apps" has a non-string key`, registryPath)
		}
		ids = append(ids, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, fmt.Errorf(`parse %s entry %q: %w`, registryPath, key, err)
		}
	}
	return ids, nil
}

// assertVerifiedEveryEntry fails unless the number of entries verified equals
// the number of app IDs literally present in the file — no duplicates, nothing
// dropped between the raw bytes and the verified set.
//
// This is the assertion that stops "verify-registry passed" from meaning
// "verify-registry looked at a subset and liked it".
func assertVerifiedEveryEntry(registryPath string, verified int) error {
	ids, err := rawAppIDs(registryPath)
	if err != nil {
		return fmt.Errorf("coverage check: %w", err)
	}
	if verified == 0 {
		return fmt.Errorf("coverage check: verified 0 entries in %s", registryPath)
	}
	seen := make(map[string]bool, len(ids))
	var dupes []string
	for _, id := range ids {
		if seen[id] {
			dupes = append(dupes, id)
		}
		seen[id] = true
	}
	if len(dupes) > 0 {
		sort.Strings(dupes)
		return fmt.Errorf("coverage check: %s declares duplicate app ID(s) %v — the shadowed "+
			"entry is never verified. Remove the duplicate", registryPath, dupes)
	}
	if len(ids) != verified {
		return fmt.Errorf("coverage check: %s contains %d app IDs but only %d were verified — "+
			"%d entry/entries were not checked", registryPath, len(ids), verified, len(ids)-verified)
	}
	return nil
}

// quarantineFile is the on-disk shape of the unverified registry, parsed
// independently of appnet.Registry: this check must see the file as it literally
// is, including the marker and the raw signature field.
type quarantineFile struct {
	Unverified *bool                      `json:"_unverified"`
	Apps       map[string]json.RawMessage `json:"apps"`
}

// crossCheckQuarantine proves the split between the signed registry and the
// unverified quarantine registry is real.
//
// It FAILS CLOSED when the quarantine file exists and is wrong:
//   - it does not declare "_unverified": true (so LoadRegistry's content-based
//     refusal would not fire if the file were renamed);
//   - it is empty (an empty quarantine must be deleted, not left as a passing
//     no-op — this programme has enough gates that pass by doing nothing);
//   - an entry in it carries a signature, i.e. is dressed up as trusted;
//   - an app ID appears in BOTH files, which would let an unsigned entry shadow
//     or impersonate a signed one.
//
// When the file does not exist, it SKIPS LOUDLY, naming exactly what was not
// cross-checked. That is a legitimate state: it means every entry has been
// promoted into the signed set.
func crossCheckQuarantine(unverifiedPath, registryPath string) error {
	data, err := os.ReadFile(unverifiedPath)
	if os.IsNotExist(err) {
		fmt.Printf("verify-registry: quarantine cross-check SKIPPED — %s does not exist, so there "+
			"are no unverified entries to keep out of %s. NOT verified by this run: "+
			"quarantine/signed disjointness, quarantine entries being unsigned, quarantine marker.\n",
			unverifiedPath, registryPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("quarantine cross-check: read %s: %w", unverifiedPath, err)
	}

	var qf quarantineFile
	if err := json.Unmarshal(data, &qf); err != nil {
		return fmt.Errorf("quarantine cross-check: parse %s: %w", unverifiedPath, err)
	}
	if qf.Unverified == nil || !*qf.Unverified {
		return fmt.Errorf("quarantine cross-check: %s does not declare %q: true — without that "+
			"marker appnet.LoadRegistry would only refuse it by filename, so renaming the file "+
			"would load unsigned entries as trusted", unverifiedPath, "_unverified")
	}
	if len(qf.Apps) == 0 {
		return fmt.Errorf("quarantine cross-check: %s exists but holds no entries — delete the "+
			"file rather than leaving an empty quarantine that makes this check pass by doing nothing",
			unverifiedPath)
	}

	// Every quarantined entry must be unsigned. A signature here means either it
	// belongs in registry.json or it is bogus; both are hard failures.
	qIDs := make([]string, 0, len(qf.Apps))
	for id := range qf.Apps {
		qIDs = append(qIDs, id)
	}
	sort.Strings(qIDs)
	for _, id := range qIDs {
		var probe struct {
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(qf.Apps[id], &probe); err != nil {
			return fmt.Errorf("quarantine cross-check: parse %s entry %q: %w", unverifiedPath, id, err)
		}
		if probe.Signature != "" {
			return fmt.Errorf("quarantine cross-check: %s entry %q carries a signature — a "+
				"quarantined entry must be unsigned. Promote it into %s (validate on real "+
				"hardware, then `make sign-registry`) or remove the signature",
				unverifiedPath, id, registryPath)
		}
	}

	// The two sets must be disjoint.
	signedIDs, err := rawAppIDs(registryPath)
	if err != nil {
		return fmt.Errorf("quarantine cross-check: %w", err)
	}
	signedSet := make(map[string]bool, len(signedIDs))
	for _, id := range signedIDs {
		signedSet[id] = true
	}
	var both []string
	for _, id := range qIDs {
		if signedSet[id] {
			both = append(both, id)
		}
	}
	if len(both) > 0 {
		return fmt.Errorf("quarantine cross-check: app ID(s) %v appear in BOTH %s and %s — an "+
			"unverified entry must never shadow a signed one. Delete the quarantined copy once "+
			"the entry is promoted", both, registryPath, unverifiedPath)
	}

	// LoadRegistry must actually refuse this file. Assert it here rather than
	// trusting the comment: this is the only thing keeping VULOS_REGISTRY from
	// pointing the App Hub at unsigned entries.
	if _, err := appnet.LoadRegistry(unverifiedPath); err == nil {
		return fmt.Errorf("quarantine cross-check: appnet.LoadRegistry ACCEPTED %s — the "+
			"quarantine is loadable as a trusted registry", unverifiedPath)
	} else if !errors.Is(err, appnet.ErrUnverifiedRegistry) {
		return fmt.Errorf("quarantine cross-check: appnet.LoadRegistry rejected %s for the wrong "+
			"reason (want ErrUnverifiedRegistry): %v", unverifiedPath, err)
	}

	fmt.Printf("verify-registry: quarantine OK — %s holds %d UNSIGNED entry/entries (%s), disjoint "+
		"from %s, marked %q: true, and refused by appnet.LoadRegistry\n",
		unverifiedPath, len(qIDs), strings.Join(qIDs, ", "), registryPath, "_unverified")
	return nil
}

// sortedAppIDs returns the registry's app IDs in a stable order so that output
// and signing order are reproducible.
func sortedAppIDs(reg *appnet.Registry) []string {
	ids := make([]string, 0, len(reg.Apps))
	for id := range reg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
