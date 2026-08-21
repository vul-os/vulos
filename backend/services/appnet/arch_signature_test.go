package appnet

// arch_signature_test.go — the publisher signature, on the listing.
//
// ─────────────────────────────────────────────────────────────────────────────
// THE GAP THIS CLOSES
//
// 55 of the 74 shipped entries carry no publisher signature, and that is the
// intended state: they were staged by a catalogue wave and the signing key is
// deliberately not on any build machine. InstallFromRegistry refuses every one
// of them and leaves nothing on disk — TestAcceptance_UnsignedShippedEntriesAre-
// Uninstallable measures exactly that, 55/55, on a prod box.
//
// The App Hub listed all 55 anyway, with a live Install button. So the security
// property was intact and the PRODUCT was not: the box advertised 55 apps whose
// install could only fail, and the refusal existed only for a user who pressed
// the button. registry.d/apt-retired.json calls that "the worse of the two
// failures".
//
// Every test here therefore measures the LISTING, not the install gate. The
// install gate already has its acceptance suite; what had never been checked is
// whether the answer the App Hub renders agrees with it.
//
// WHY THIS FILE DOES NOT USE arm64Box. That helper turns signature verification
// off, because everything in arch_availability_test.go and arch_emulation_test.go
// is about architecture. Here the signature IS the subject, so these tests stage
// real trust anchors and sign fixtures with real Ed25519 keys — the same
// SignEntry/VerifyEntrySignature pair the install path uses.

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// entryFor builds a small native-arch entry: on an arm64 box it would be rung 1,
// installable, badge-free. Anything this box then refuses about it is the
// signature and nothing else.
func entryFor(name string) *RegistryEntry {
	return &RegistryEntry{
		Name: name, Type: "desktop", Arch: []string{"arm64"},
		Versions: map[string]*VersionRecipe{"1.0": {FlatpakID: "org.example." + strings.ToLower(name)}},
	}
}

// ── the verdict ──────────────────────────────────────────────────────────────

// TestEvaluateArch_UnsignedEntryIsNotOffered is the property this work exists
// for: an entry with no publisher signature must not be offered as installable,
// and must say why in words the hub can render.
func TestEvaluateArch_UnsignedEntryIsNotOffered(t *testing.T) {
	// Native arch on this box — so the ONLY thing that can refuse it is the
	// signature. A wrong-arch fixture would pass this test without the gate.
	req := ArchRequest{
		AppName: "Ardour", Declared: []string{"arm64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak, Signature: SignatureUnsigned,
	}
	av := EvaluateArch(req)

	if av.Installable {
		t.Fatalf("an UNSIGNED entry is offered as installable: %+v.\n"+
			"InstallFromRegistry refuses it before anything is downloaded, so this is an "+
			"Install button that can only fail", av)
	}
	if av.Signature != SignatureUnsigned {
		t.Errorf("the verdict does not carry the signature state it was given: %q", av.Signature)
	}
	if av.Badge == "" || av.CardBadge == "" || av.Detail == "" {
		t.Fatalf("refused with no words for it: %+v — the App Hub renders these verbatim and "+
			"composes no sentence of its own", av)
	}

	// The control. Same entry, same box, signature verified: it must be offered.
	// Without this the test passes on a gate that refuses EVERYTHING.
	req.Signature = SignatureSigned
	if ctl := EvaluateArch(req); !ctl.Installable || ctl.State != ArchStateNative {
		t.Fatalf("the control is refused too, so nothing above is attributable to the "+
			"signature: %+v", ctl)
	}
}

// TestEvaluateArch_UnsetSignatureFailsClosed. ArchRequest.Signature's zero value
// is "", and a caller that forgets the field must get the verdict that offers
// LESS — the same choice EmulatorBindsHostLibraries makes. The opposite default
// would turn every future caller's omission into a live Install button on an
// entry no key has ever covered.
func TestEvaluateArch_UnsetSignatureFailsClosed(t *testing.T) {
	av := EvaluateArch(ArchRequest{
		AppName: "Ardour", Declared: []string{"arm64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak, // Signature deliberately unset
	})
	if av.Installable {
		t.Fatalf("a request that says nothing about the signature is offered as installable: %+v",
			av)
	}
	if av.Signature != SignatureUnsigned {
		t.Errorf("the unset zero value resolved to %q, want %q — an empty string on the wire "+
			"would be a fifth, undeclared state the App Hub has never heard of",
			av.Signature, SignatureUnsigned)
	}
}

// TestEvaluateArch_SignatureOutranksArchitecture is the PRECEDENCE decision.
//
// An entry can be both unsigned and wrong-arch; on this arm64 box most of the 55
// are. Only one of the two facts can lead, and it is the signature:
//
//  1. It is the gate the install actually hits. InstallFromRegistry runs
//     VerifyEntrySignature BEFORE ArchSupported, so an arch-first card would
//     describe an error message the user would never see.
//  2. The arch sentence would be FALSE. Rung 5 closes with "It stays available
//     on any amd64 instance you run" — and while the entry is unsigned, no
//     instance of any architecture can install it.
//
// So this test checks both halves: the signature words are what the reader gets,
// AND the fleet-wide promise rung 5 would have made is absent.
func TestEvaluateArch_SignatureOutranksArchitecture(t *testing.T) {
	both := ArchRequest{
		AppName: "Steam", Declared: []string{"amd64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak, Signature: SignatureUnsigned,
	}
	av := EvaluateArch(both)

	if av.Installable {
		t.Fatalf("offered: %+v", av)
	}
	if !strings.Contains(strings.ToLower(av.Badge), "signature") {
		t.Errorf("badge = %q — an entry that is BOTH unsigned and wrong-arch must be told about "+
			"the gate its install actually hits first, which is the signature", av.Badge)
	}
	if av.CardBadge == "Needs amd64" {
		t.Errorf("card badge = %q: the chip names the architecture on an entry that no box of "+
			"any architecture can install", av.CardBadge)
	}

	// The claim that must not survive. Rung 5's closing clause promises the app
	// to the rest of the fleet, and an unsigned entry is refused everywhere.
	for _, promise := range []string{"stays available on any", "instance you run"} {
		if strings.Contains(av.Detail, promise) {
			t.Errorf("the sentence promises %q while the entry is unsigned — no instance of any "+
				"architecture can install it:\n  %s", promise, av.Detail)
		}
	}

	// The architecture is NOT lost, it is just not the headline: the detail
	// panel's "Architecture" row renders Needs exactly as it does for a signed
	// entry, and the box still names itself.
	if len(av.Needs) != 1 || av.Needs[0] != "amd64" {
		t.Errorf("needs = %v, want [amd64] — short-circuiting on the signature dropped the "+
			"architecture the panel still shows", av.Needs)
	}
	if av.BoxArch != "arm64" {
		t.Errorf("box_arch = %q", av.BoxArch)
	}

	// The control: signed, same box, same entry — now the ARCH refusal is what
	// the reader gets, complete with the fleet clause that is true again.
	both.Signature = SignatureSigned
	ctl := EvaluateArch(both)
	if ctl.CardBadge != "Needs amd64" {
		t.Fatalf("once signed, the arch refusal must come back: card badge = %q", ctl.CardBadge)
	}
	if !strings.Contains(ctl.Detail, "stays available on any") {
		t.Fatalf("once signed, rung 5's fleet clause must come back: %s", ctl.Detail)
	}
}

// TestEvaluateArch_TheThreeSignatureHoldsReadDifferently.
//
// The same split as the two emulation refusals: "not signed yet", "signed by
// someone this box does not trust" and "this box cannot check anything" are
// three different facts about three different things — the catalogue, the entry,
// and the box — and one wording for all three is false for two of them.
func TestEvaluateArch_TheThreeSignatureHoldsReadDifferently(t *testing.T) {
	base := ArchRequest{
		AppName: "Ardour", Declared: []string{"arm64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak,
	}
	seen := map[string]ArchAvailability{}
	for _, sig := range []string{SignatureUnsigned, SignatureUntrusted, SignatureUncheckable} {
		req := base
		req.Signature = sig
		av := EvaluateArch(req)
		if av.Installable {
			t.Fatalf("%s: offered as installable: %+v", sig, av)
		}
		if av.Signature != sig {
			t.Errorf("%s: verdict carries %q", sig, av.Signature)
		}
		seen[sig] = av
	}

	for _, pair := range [][2]string{
		{SignatureUnsigned, SignatureUntrusted},
		{SignatureUnsigned, SignatureUncheckable},
		{SignatureUntrusted, SignatureUncheckable},
	} {
		a, b := seen[pair[0]], seen[pair[1]]
		if a.Detail == b.Detail {
			t.Errorf("%s and %s give the SAME sentence:\n  %s\nOne of the two readers is being "+
				"told something false about their box or their catalogue", pair[0], pair[1], a.Detail)
		}
		if a.CardBadge == b.CardBadge {
			t.Errorf("%s and %s give the same card badge %q", pair[0], pair[1], a.CardBadge)
		}
	}

	// A tampered entry must never be described as merely waiting for a ceremony.
	if strings.Contains(strings.ToLower(seen[SignatureUntrusted].Detail), "awaiting") ||
		strings.Contains(strings.ToLower(seen[SignatureUntrusted].Detail), "not been signed") {
		t.Errorf("an entry whose signature FAILED is described as pending: %s",
			seen[SignatureUntrusted].Detail)
	}
}

// TestSignatureCopy_MakesNoClaimTheBoxCannotKeep sweeps the signature copy the
// same way TestEvaluateArch_NoUnmeasuredClaimReachesTheUser sweeps the arch
// copy, and adds the four rules that are specific to a signature hold.
func TestSignatureCopy_MakesNoClaimTheBoxCannotKeep(t *testing.T) {
	base := ArchRequest{
		AppName: "Ardour", Declared: []string{"arm64"}, Supported: []string{"arm64"},
		Delivery: DeliveryFlatpak,
	}
	holds := []string{SignatureUnsigned, SignatureUntrusted, SignatureUncheckable}
	for _, sig := range holds {
		req := base
		req.Signature = sig
		av := EvaluateArch(req)

		for _, field := range []struct{ what, s string }{
			{"badge", av.Badge}, {"card_badge", av.CardBadge}, {"detail", av.Detail},
		} {
			low := strings.ToLower(field.s)
			for _, banned := range unmeasuredClaims {
				if strings.Contains(low, banned.phrase) {
					t.Errorf("%s: %s says %q.\n  %s\n  full text: %s",
						sig, field.what, banned.phrase, banned.why, field.s)
				}
			}
			// (1) It is not broken. Nothing is wrong with the software; a step
			// in publishing it has not happened.
			for _, wrong := range []string{"broken", "corrupt", "faulty"} {
				if strings.Contains(low, wrong) {
					t.Errorf("%s: %s calls the app %q — a signature hold is a fact about the "+
						"CATALOGUE, and the software is not implicated: %s",
						sig, field.what, wrong, field.s)
				}
			}
			// (2) It must not point at a control the reader does not have. The
			// signing key is deliberately not on this machine, so any
			// instruction here is the retired "turn it on in Settings" defect
			// wearing a new hat.
			for _, instruction := range []string{"you can sign", "sign it yourself", "run `make sign",
				"contact support", "try again later"} {
				if strings.Contains(low, instruction) {
					t.Errorf("%s: %s tells the reader to %q, and they cannot: %s",
						sig, field.what, instruction, field.s)
				}
			}
		}
		// (3) Never the bare word "Unavailable", for the same reason rung 5 is
		// not allowed it: it reads as broken rather than as a state.
		if av.Badge == "Unavailable" || av.CardBadge == "Unavailable" {
			t.Errorf("%s: the bare word \"Unavailable\"", sig)
		}
		// (4) It must not promise that a signature makes THIS box able to
		// install it. Most of the 55 are also amd64-only.
		for _, promise := range []string{"will install here", "installable here once",
			"this box will install it once"} {
			if strings.Contains(strings.ToLower(av.Detail), promise) {
				t.Errorf("%s: the sentence promises %q — signing is a NECESSARY condition, not a "+
					"sufficient one, and most held entries are also wrong-arch for this box:\n  %s",
					sig, promise, av.Detail)
			}
		}
	}
	if len(holds) < 3 {
		t.Fatalf("the sweep covers %d holds — it is the coverage", len(holds))
	}
}

// ── the classifier, against the gate it must agree with ──────────────────────

// TestEntrySignatureState_AgreesWithTheInstallGate is the anti-drift assertion,
// and it is the one that matters most: the listing's verdict and the install's
// refusal must be the same answer, because a hub that offers what the box
// refuses is the defect, and a hub that refuses what the box would install is
// the same defect pointed the other way.
//
// It drives the REAL InstallFromRegistry. Safe without a network: the signature
// gate is the first thing it does after resolving the version, so a refused
// entry never reaches a download, and the one entry that clears the gate is
// refused a step later for its recipe.
func TestEntrySignatureState_AgreesWithTheInstallGate(t *testing.T) {
	pub, priv := generateTestKey(t)
	withTrustAnchor(t, pub)
	_, otherPriv := generateTestKey(t)

	signed := entryFor("Signed")
	if err := SignEntry(signed, "signed", priv); err != nil {
		t.Fatalf("sign: %v", err)
	}
	unsigned := entryFor("Unsigned")
	foreign := entryFor("Foreign")
	if err := SignEntry(foreign, "foreign", otherPriv); err != nil {
		t.Fatalf("sign with the foreign key: %v", err)
	}
	rekeyed := entryFor("Rekeyed")
	if err := SignEntry(rekeyed, "some-other-slot", priv); err != nil {
		t.Fatalf("sign for the wrong slot: %v", err)
	}

	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("resolve trust: %v", err)
	}

	cases := []struct {
		appID string
		entry *RegistryEntry
		want  string
	}{
		{"signed", signed, SignatureSigned},
		{"unsigned", unsigned, SignatureUnsigned},
		{"foreign", foreign, SignatureUntrusted},
		{"rekeyed", rekeyed, SignatureUntrusted},
	}
	reg := &Registry{Apps: map[string]*RegistryEntry{}}
	for _, c := range cases {
		reg.Apps[c.appID] = c.entry
	}

	seen := map[string]bool{}
	for _, c := range cases {
		got := EntrySignatureState(c.appID, c.entry, key, nil)
		if got != c.want {
			t.Errorf("%s: classified %q, want %q", c.appID, got, c.want)
		}
		seen[got] = true

		// And now the half that no classifier test can fake: does the INSTALL
		// agree? "Refused for its signature" and "not classified signed" must be
		// the same set.
		err := InstallFromRegistry(context.Background(), reg, c.appID, "", t.TempDir())
		refusedForSignature := err != nil && strings.Contains(err.Error(), "signature")
		if refusedForSignature != (got != SignatureSigned) {
			t.Errorf("%s: the listing says %q and the install %s.\n"+
				"The card and the button are describing two different gates: %v",
				c.appID, got,
				map[bool]string{true: "refused it for its signature", false: "did not"}[refusedForSignature],
				err)
		}
	}
	for _, want := range []string{SignatureSigned, SignatureUnsigned, SignatureUntrusted} {
		if !seen[want] {
			t.Fatalf("the table never produced %q, so nothing above was applied to it: %v", want, seen)
		}
	}
}

// TestEntrySignatureState_UncheckableBoxRefusesEverything. PreflightTrust's
// Degraded verdict lets a box with no trust anchor keep SERVING while every
// install is refused. Before this work that box listed all 74 entries as
// installable — the same defect as the 55, at full catalogue width.
func TestEntrySignatureState_UncheckableBoxRefusesEverything(t *testing.T) {
	// A box with nothing configured: no anchor file, no pubkey, no escape hatch.
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryPubKey, "")
	t.Setenv(envRegistryInsecure, "")
	isolateTrustSources(t)

	if _, err := TrustedKey(); err == nil {
		t.Fatal("this box resolved a trust key, so it is not the degraded box this test is about")
	}

	entry := entryFor("Ardour")
	reg := &Registry{Apps: map[string]*RegistryEntry{"ardour": entry}}
	entries := reg.ListEntries(t.TempDir())
	if len(entries) != 1 {
		t.Fatalf("got %d entries", len(entries))
	}
	av := entries[0].Availability
	if av.Signature != SignatureUncheckable {
		t.Errorf("signature = %q, want %q", av.Signature, SignatureUncheckable)
	}
	if av.Installable {
		t.Fatalf("a box that can check no signature at all offers an install: %+v", av)
	}
	if entries[0].InstallableReason != av.Detail {
		t.Errorf("installable_reason %q is not the availability sentence %q",
			entries[0].InstallableReason, av.Detail)
	}
}

// TestEntrySignatureState_InsecureModeAgreesWithTheInstall. A developer who sets
// VULOS_REGISTRY_INSECURE=1 has done so precisely in order to install unsigned
// entries. A listing that refused them anyway would be a hub disagreeing with
// its own box — the same drift, pointed the other way.
func TestEntrySignatureState_InsecureModeAgreesWithTheInstall(t *testing.T) {
	withInsecureRegistry(t)
	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("insecure mode returned an error: %v", err)
	}
	if key != nil {
		t.Fatal("insecure mode returned a key; this test is not measuring the skip path")
	}
	if got := EntrySignatureState("ardour", entryFor("Ardour"), key, nil); got != SignatureSigned {
		t.Errorf("an unsigned entry on a deliberately insecure box classifies as %q, so the hub "+
			"refuses what the box would install", got)
	}
}

// ── the listing, on the catalogue this repo actually ships ───────────────────

// TestListEntries_UnsignedShippedEntriesAreNotOffered is the honest end of this
// work, measured against the registry.json that ships rather than a fixture, on
// a box staged with the anchor and release cert this repo ships.
//
// COVERAGE, both ways, because this file's whole subject is a catalogue in a
// mixed state: if every entry were signed the unsigned loop would examine
// nothing, and if none were the control on the signed half would.
func TestListEntries_UnsignedShippedEntriesAreNotOffered(t *testing.T) {
	anchor, cert := shippedTrust(t)
	stageBox(t, "prod", anchor, &cert)
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t)
	SetEmulationOptedIn(false)
	t.Cleanup(func() { SetEmulationOptedIn(false) })

	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("prod box could not resolve the shipped trust chain: %v", err)
	}

	raw, err := os.ReadFile(shippedRegistryPath(t))
	if err != nil {
		t.Fatalf("cannot read the registry this gate exists to walk: %v", err)
	}
	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("registry.json did not parse: %v", err)
	}
	if len(reg.Apps) < registryTotalFloor {
		t.Fatalf("examined %d entries, floor is %d — a shrinking registry would make this gate "+
			"pass by looking at nothing", len(reg.Apps), registryTotalFloor)
	}
	st := partitionShippedRegistry(t, &reg, key)

	byID := map[string]RegistryListEntry{}
	for _, e := range reg.ListEntries(t.TempDir()) {
		byID[e.ID] = e
	}
	if len(byID) != len(reg.Apps) {
		t.Fatalf("listed %d of %d entries", len(byID), len(reg.Apps))
	}

	var offered []string
	for _, appID := range st.unsigned {
		e, ok := byID[appID]
		if !ok {
			t.Fatalf("%s is in the registry and not in the listing", appID)
		}
		av := e.Availability
		if av.Signature != SignatureUnsigned {
			t.Errorf("%s: listed as %q though its entry carries no signature", appID, av.Signature)
		}
		if av.Installable || e.Installable {
			offered = append(offered, appID)
			continue
		}
		if av.Badge == "" || av.CardBadge == "" || av.Detail == "" {
			t.Errorf("%s: refused with no words for it: %+v", appID, av)
		}
		for _, s := range []string{av.Badge, av.CardBadge, av.Detail} {
			low := strings.ToLower(s)
			for _, banned := range unmeasuredClaims {
				if strings.Contains(low, banned.phrase) {
					t.Errorf("%s: shipped copy says %q.\n  %s\n  full text: %s",
						appID, banned.phrase, banned.why, s)
				}
			}
		}
	}
	if len(offered) > 0 {
		sort.Strings(offered)
		t.Errorf("%d UNSIGNED shipped entries are offered as installable: %v.\n"+
			"Every one of those Install buttons can only fail — InstallFromRegistry refuses "+
			"them before anything is downloaded", len(offered), offered)
	}

	// COVERAGE 1: the unsigned path must ALWAYS be exercised, in every state
	// registry.json can be in.
	//
	// This used to `t.Fatal` when the registry held no unsigned entry, which
	// made the test fail on the one state the project is working towards: a
	// fully-signed catalogue. Measured 2026-08-19 by running the real ceremony
	// against a 142-entry registry in a throwaway worktree — 142/142 signed,
	// and this was the ONLY test still red. A gate that goes red the moment the
	// founder signs is a gate that would be discovered mid-ceremony, with the
	// offline key on the table, which is the worst possible time.
	//
	// So it now carries its own signature-stripped control, exactly as the note
	// it used to print told the next reader to do, and exactly as
	// TestAcceptance_UnsignedShippedEntriesAreUninstallable already did.
	// `controlEntryID` picks a real entry (preferring a verified one, because
	// stripping a signature that demonstrably worked is the sharpest control)
	// and works whether or not any unsigned entry remains.
	control := controlEntryID(t, st, &reg)
	// Copy the struct before stripping. reg.Apps holds POINTERS, so mutating
	// the entry in place would blank a signature in the registry every other
	// assertion in this test is still reading — the control would corrupt its
	// own subject. Only Signature (a string) is touched, so the shallow copy
	// is sufficient and the shared slices stay untouched.
	orig := reg.Apps[control]
	stripped := *orig
	stripped.Signature = ""
	controlReg := Registry{Apps: map[string]*RegistryEntry{control: &stripped}}
	controlList := controlReg.ListEntries(t.TempDir())
	if len(controlList) != 1 {
		t.Fatalf("control: listing a one-entry registry produced %d entries", len(controlList))
	}
	cav := controlList[0].Availability
	if cav.Signature != SignatureUnsigned {
		t.Errorf("control %q: signature stripped, but listed as %q — the listing is not "+
			"reading the signature it claims to read", control, cav.Signature)
	}
	if cav.Installable || controlList[0].Installable {
		t.Errorf("control %q: signature stripped and still offered as installable. "+
			"That Install button can only fail.", control)
	}

	// COVERAGE 2: the signed half must still reach the architecture rungs.
	// Without this, a gate that refused the WHOLE catalogue would pass every
	// assertion above while hiding the entire listing behind one wrong verdict.
	//
	// It fires only when there IS a verified entry this box could run. Before
	// the ceremony the verified set is small and may be entirely foreign-arch —
	// that is a fact about the data, not a fault in the gate, and failing on it
	// would report the wrong thing. `archEligible` is what makes the difference
	// between "the gate is refusing everything" and "there is nothing here for
	// this box yet".
	native, archEligible := 0, 0
	for _, appID := range st.verified {
		av := byID[appID].Availability
		if av.Signature != SignatureSigned {
			t.Errorf("%s: verified against the shipped anchor and listed as %q", appID, av.Signature)
		}
		if av.State == ArchStateNative {
			archEligible++
			if av.Installable {
				native++
			}
		}
	}
	if archEligible > 0 && native == 0 {
		t.Errorf("%d verified entries are native to this arm64 box and NOT ONE is offered — "+
			"the signature gate is refusing the whole catalogue", archEligible)
	}

	t.Logf("shipped registry on a prod arm64 box: %d entries — %d verified (%d of %d arch-native "+
		"offered), %d unsigned and held, %d invalid; plus the signature-stripped control %q",
		st.total, len(st.verified), native, archEligible,
		len(st.unsigned), len(st.invalid), control)
}

// ── the wire ─────────────────────────────────────────────────────────────────

// TestListEntries_SignatureSurvivesJSON. The App Hub reads JSON, not a Go
// struct, and it narrows `availability` field by field. A signature verdict that
// does not marshal is a card that renders as if the box never answered — which
// arch.ts folds to "offered, no claim attached".
func TestListEntries_SignatureSurvivesJSON(t *testing.T) {
	pub, _ := generateTestKey(t)
	withTrustAnchor(t, pub)
	withBoxArch(t, "arm64")
	withNoBinfmt(t)
	withEmulatorsOnPath(t)

	reg := &Registry{Apps: map[string]*RegistryEntry{"ardour": entryFor("Ardour")}}
	raw, err := json.Marshal(reg.ListEntries(t.TempDir()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []struct {
		Availability map[string]any `json:"availability"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries", len(out))
	}
	got, ok := out[0].Availability["signature"]
	if !ok {
		t.Fatal("availability.signature is missing from the wire shape — the App Hub cannot " +
			"tell a signature hold from an architecture refusal and would style one as the other")
	}
	if got != SignatureUnsigned {
		t.Errorf("availability.signature = %v, want %q", got, SignatureUnsigned)
	}
	if out[0].Availability["installable"] != false {
		t.Errorf("availability.installable = %v on an unsigned entry", out[0].Availability["installable"])
	}
}
