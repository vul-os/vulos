package appnet

// arch_entry.go — the bridge between a REGISTRY ENTRY and EvaluateArch.
//
// ─────────────────────────────────────────────────────────────────────────────
// WHY THIS FILE EXISTS
//
// EvaluateArch takes an ArchRequest whose every field is supplied by the caller,
// deliberately: the probes behind them have different lifetimes (SupportedArches
// is per-listing and may shell out to flatpak; the emulator probe is cached; the
// registry data is per-entry). That design is right and it had one consequence
// nobody paid — until 2026-08-17 NOTHING BUILT AN ArchRequest. EvaluateArch,
// EmulationCanInstall, EmulationRunsWell, DeliveryKindOf, ParseEmulationPolicy
// and the binfmt parser had zero callers repo-wide, and the App Hub decided
// availability itself in the browser with a raw string comparison.
//
// This file is the missing half. It reads the three per-entry facts EvaluateArch
// needs and that only registry data can answer, so ListEntries can hand the App
// Hub ONE answer instead of two lists to compare for itself.
//
// ─────────────────────────────────────────────────────────────────────────────
// WHY IT READS `Extra` RATHER THAN ADDING STRUCT FIELDS
//
// `lane` and `emulation_policy` are not modelled on RegistryEntry; they land in
// Extra, the passthrough map that keeps unmodelled keys inside the publisher
// signature. Promoting either to a struct field would move it from Extra into
// the marshalled base, and the publisher signature is taken over the marshalled
// entry — so a schema change there is a signing question, and 55 of the 74
// shipped entries are unsigned pending a human ceremony. Reading the key where
// it already lives changes no byte of any entry.
//
// The thing to be careful about is the OTHER failure: `lane.needs_gpu` is set on
// eight shipped entries and, before this file, was read by nothing at all. That
// is precisely the per-recipe `arch` shape (APP-RECIPE-STANDARD §1.1) — a field
// that looks like it does something and does not. It does something now.

import "encoding/json"

// EntryDeliveryKind classifies how an entry's bits would reach this box, by
// looking at its LATEST recipe.
//
// Latest and not "any recipe": an install offers the latest version by default,
// and a listing that judged emulation on a two-year-old recipe's shape would
// describe an install the user cannot start. An entry with no recipes at all is
// DeliveryUnknown, which EmulationCanInstall refuses — the zero value must not
// acquire a path nobody classified.
func EntryDeliveryKind(entry *RegistryEntry) DeliveryKind {
	if entry == nil {
		return DeliveryUnknown
	}
	recipe := entry.GetRecipe(entry.LatestVersion())
	if recipe == nil {
		return DeliveryUnknown
	}
	return DeliveryKindOf(recipe.FlatpakID, recipe.DownloadURL, len(recipe.Artifacts), recipe.Install)
}

// entryLane is the shape of the `lane` object shipped entries carry.
type entryLane struct {
	NeedsGPU bool `json:"needs_gpu"`
}

// EntryNeedsGPU reports exception E3's input: does this app need graphics
// acceleration to be worth offering at all.
//
// It is NOT on its own a refusal — see EvaluateArch's third switch arm, which
// refuses only when the emulation available HERE cannot bind this box's own
// graphics driver. box64 was measured doing exactly that and qemu-user was
// measured failing to obtain a GL visual at all, so the same app is a different
// answer on two boxes and this function must not pretend otherwise.
//
// Malformed `lane` is false rather than an error: an entry whose lane object is
// the wrong shape has said nothing about GPUs, and the direction that offers
// less is the one that would refuse an app the user could have had. False here
// only ever moves the decision to the arms below it, each of which has its own
// reason and its own sentence.
func EntryNeedsGPU(entry *RegistryEntry) bool {
	if entry == nil {
		return false
	}
	raw, ok := entry.Extra["lane"]
	if !ok {
		return false
	}
	var lane entryLane
	if err := json.Unmarshal(raw, &lane); err != nil {
		return false
	}
	return lane.NeedsGPU
}

// EntryEmulationPolicy reports whether the entry has been cleared to be offered
// under emulation (ARCH-PLACEMENT §8.3).
//
// NO SHIPPED ENTRY DECLARES THIS TODAY, and that is worth stating rather than
// discovering: every entry therefore parses as EmulationNever and rung 3 is
// unreachable for the current catalogue. That is the correct answer while the
// distribution-sourced vehicle is parked (roadmap/DISTRO-SOURCED-APPS.md §8) —
// what it is not is a reason to skip the path, because the alternative is a
// decision layer that stays untested until the first entry opts in.
func EntryEmulationPolicy(entry *RegistryEntry) EmulationPolicy {
	if entry == nil {
		return EmulationNever
	}
	raw, ok := entry.Extra["emulation_policy"]
	if !ok {
		return EmulationNever
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return EmulationNever
	}
	return ParseEmulationPolicy(s)
}

// archEnvironment is everything about THE BOX that EvaluateArch needs, resolved
// once for a whole listing rather than per entry.
//
// Resolving per entry would be wrong twice over: SupportedArches may shell out
// to flatpak, and — the part that is not about speed — two entries of one
// response could then disagree about what machine they are on, which is
// unreadable from the outside and would look like a rendering fault.
type archEnvironment struct {
	supported []string
	emulated  []string
	optedIn   bool
}

func currentArchEnvironment() archEnvironment {
	return archEnvironment{
		supported: SupportedArches(),
		emulated:  EmulatedArches(),
		optedIn:   EmulationOptedIn(),
	}
}

// evaluate answers one entry against this box.
//
// OtherInstance is EMPTY here and that is a stated gap, not an oversight.
// Rung 4 needs to know that a synced sibling is running this app, which lives in
// the app_registry realisation table replicated by internal/multiinstance;
// services/appnet cannot see it (it only satisfies multiinstance.Realiser
// structurally, in the other direction). EvaluateArch implements rung 4 and its
// tests exercise it; nothing in production populates it yet, so on this box a
// refused app renders rung 5. Wiring it means passing a lookup down from the
// composition root, which is main.go's to do.
func (env archEnvironment) evaluate(entry *RegistryEntry) ArchAvailability {
	declared := entry.Arch
	return EvaluateArch(ArchRequest{
		AppName:   entry.Name,
		Declared:  declared,
		Delivery:  EntryDeliveryKind(entry),
		Policy:    EntryEmulationPolicy(entry),
		NeedsGPU:  EntryNeedsGPU(entry),
		Supported: env.supported,
		Emulated:  env.emulated,
		// Which emulator would serve THIS app: box64 and qemu-user cover the
		// same architecture and gave opposite GL answers under the same
		// harness, so asking "is an emulator present" is not enough.
		EmulatorBindsHostLibraries: env.bindsHostLibrariesFor(declared),
		EmulationEnabled:           env.optedIn,
		OtherInstance:              "",
	})
}

// bindsHostLibrariesFor reports whether any architecture this app declares has
// an emulator here that substitutes the box's own native libraries.
//
// An "any" over the declared arches, matching EmulationBindsHostLibrariesFor's
// own "any" over emulators: an app declaring amd64 and riscv64 on a box with
// box64 can be served by box64, and the absence of a riscv64 emulator does not
// take that away.
func (env archEnvironment) bindsHostLibrariesFor(declared []string) bool {
	for _, d := range declared {
		if EmulationBindsHostLibrariesFor(d) {
			return true
		}
	}
	return false
}
