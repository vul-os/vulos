package sqlcrdt

// osstate.go — the OS-wide state inventory.
//
// # Why this lives here
//
// tables.go already answers "which SQL tables does the CRDT engine carry".
// That is a strict SUBSET of "which OS state does a user expect to find on
// their other box", and the difference between the two is the entire subject
// of the founder directive:
//
//	EVERYTHING MUST SYNC, EACH INSTANCE IS ALMOST A DIRECT CLONE OF NEXT
//	WITH FEW EXCEPTIONS
//
// Under that directive a state that does not sync is not a neutral fact; it is
// an EXCEPTION, and an exception has to be argued for. So this file enumerates
// every piece of state the OS keeps, says where it lives, which mechanism (if
// any) moves it between a user's boxes, and classifies it as one of three
// things: it syncs, it is a deliberate exception, or it is a GAP — state the
// directive says should follow the user and does not.
//
// # Why it is code and not prose
//
// roadmap/SYNC-INVENTORY.md says the same things in readable form, but a
// document cannot fail a build. This file can, and osstate_test.go makes it:
//
//   - every entry names a real file in this repository, and an Anchor string
//     that must literally appear in that file. An entry whose evidence has
//     moved or was never true goes red. This is the part that stops the
//     inventory decaying into the confident fiction it would otherwise
//     become — roughly a dozen false status claims have already been found
//     and corrected in this project.
//   - the crdtsync policy and this inventory must agree in both directions.
//   - a minimum entry count is asserted, so the guard cannot pass by
//     examining nothing (this repo's dominant defect class).
//   - the installed-app gap is pinned by a source scan rather than by
//     assertion, so it goes red when someone FIXES it as well as when
//     someone falsely claims it is fixed.
//
// # What this file is NOT
//
// It is not a mechanism. Nothing reads it at runtime; adding an entry does not
// make anything sync. It is a ledger with a test attached, and the work of
// actually carrying this state is specified in roadmap/SYNC-INVENTORY.md.

// SyncStatus classifies one piece of OS state against the directive.
type SyncStatus string

const (
	// StatusSyncs means this state reaches a user's other instances today,
	// through the named Engine. Conditions (LAN only, S3 required) belong in
	// Note — "syncs, but only when X" is still Syncs, and the condition is
	// what a reader needs.
	StatusSyncs SyncStatus = "syncs"

	// StatusPartial means some of this state crosses and some does not, or it
	// crosses in one direction only. Partial is never a resting place; it is a
	// gap with a head start.
	StatusPartial SyncStatus = "partial"

	// StatusException means this state DELIBERATELY does not sync, and Why
	// carries the argument. The bar is high on purpose: "we never got to it"
	// is a gap, not an exception. A legitimate exception is one where syncing
	// would be WRONG — a security defect, or a description of hardware the
	// other box does not have.
	StatusException SyncStatus = "exception"

	// StatusGap means the directive says this should follow the user and it
	// does not. Every gap here is a defect with a user-visible consequence,
	// recorded in Consequence in the words of what the user experiences.
	StatusGap SyncStatus = "gap"
)

// Engine names the mechanism that carries a state. The plurality of these is
// itself a finding — see roadmap/SYNC-INVENTORY.md's engine count.
const (
	// EngineCRDT is internal/crdtsync + internal/sqlcrdt: per-column LWW over
	// a hybrid logical clock, capturing SQLite writes via the session
	// extension. The general engine, carrying four tables.
	EngineCRDT = "crdtsync+sqlcrdt"

	// EngineAppSync is internal/multiinstance/appsync.go merged over
	// internal/fabric: a SECOND, hand-rolled CRDT for one table.
	EngineAppSync = "multiinstance/appsync+fabric"

	// EngineFileSync is services/sync: fsnotify -> S3 -> pull, at file
	// granularity, over two directories.
	EngineFileSync = "services/sync (S3 file sync)"

	// EngineSnapshot is services/sync's Compactor/Restorer: a whole-DB-file
	// snapshot of ONE database to S3.
	EngineSnapshot = "services/sync (S3 DB snapshot)"

	// EngineRevSync is services/devicekey/revsync.go: a pull loop that merges
	// device-key revocations between boxes. A purpose-built replicator for one
	// security record.
	EngineRevSync = "devicekey/revsync"

	// EnginePeerShare is services/files/peershare.go: a bespoke
	// cross-instance transfer for individually shared files. It is a SHARE
	// mechanism, not a replication one — it moves what a user explicitly
	// hands over, not what they own.
	EnginePeerShare = "files/peershare"

	// EngineNone means nothing carries this state.
	EngineNone = "none"
)

// StateEntry is one piece of OS state.
type StateEntry struct {
	// Name is the state, in the words a user would use.
	Name string
	// Where is the physical storage: db file + table, path, or storage key.
	Where string
	// Engine is the mechanism that carries it, or EngineNone.
	Engine string
	// Status classifies it against the directive.
	Status SyncStatus
	// Domain, when non-empty, is the crdtsync domain this corresponds to. It
	// ties the inventory to policy.go so the two cannot drift.
	Domain string
	// Evidence is a repository-relative path that must EXIST.
	Evidence string
	// Anchor is a string that must literally appear in Evidence. This is what
	// makes the entry checkable rather than merely plausible.
	Anchor string
	// Why is the reasoning. Required for every entry; for an exception it is
	// the argument for the exception, for a gap it is why it is a gap.
	Why string
	// Consequence is what a user experiences. Required for gaps and partials:
	// a gap stated as a user-visible sentence is much harder to wave away
	// than one stated as a missing table.
	Consequence string
	// Note carries conditions — "only over the LAN", "only when S3 is
	// configured" — that a bare status would hide.
	Note string
}

// OSStateInventory is every piece of OS state, and whether it syncs.
//
// Ordered by the founder directive's own list — install, configure, theme,
// arrange, store — then identity, then security material, then node-local
// hardware state, so a reader meets the user-facing gaps first.
func OSStateInventory() []StateEntry {
	return []StateEntry{
		// ── what you install ────────────────────────────────────────────────
		{
			Name:     "installed app set",
			Where:    "<root>/apps/<appID>/app.json — a DIRECTORY on local disk, now reconciled against a replicated record.",
			Engine:   EngineAppSync,
			Status:   StatusPartial,
			Evidence: "backend/services/appnet/store.go",
			Anchor:   "func (s *AppStore) RealisedVersions()",
			Why: "\"Installed\" is still a filesystem fact — Installed() is a scan and hasApp() is an os.Stat, and that is correct: it " +
				"is the ground truth a record can be checked against. What changed (SYNC-APPS-01) is that the disk is no longer the ONLY " +
				"copy. AppStore now satisfies multiinstance.Realiser (RealisedVersions/Realise/Unrealise), so a fleet DESIRED set drives " +
				"installs and removals here and this box reports back what it managed. " +
				"PARTIAL, not syncing, and the remainder is deliberate rather than unfinished: bundled apps ship with the image and are " +
				"never installed into <root>/apps; an app already on disk that the desired set has never heard of is left ALONE (un-adopted, " +
				"not undesired — deleting a user's pre-existing apps because a table is new would be the worst reading of the directive); " +
				"version skew is not reconciled, because upgrades need their own ordering and rollback story; and an install this box cannot " +
				"perform stays absent by definition. Two boxes therefore converge on INTENT, not byte-for-byte on <root>/apps.",
			Consequence: "Install an app on your laptop instance and your other instance now learns about it and installs it on its next " +
				"reconcile pass. Where it cannot — wrong architecture, failed download — it records the reason on a replicated row instead " +
				"of the app being silently missing. What still differs between boxes is bundled apps, versions, and anything installed " +
				"before this existed.",
			Note: "Unproven on this machine: a real two-box install over a real fabric connection. The merge, the desire algebra, the " +
				"reconciler and the AppStore seam are proven in-process; ip netns is Linux-only and the install path downloads from a " +
				"signed registry, so the end-to-end run belongs on a Linux box.",
		},
		{
			Name:     "installed app set (the replicated mirror)",
			Where:    "app_desired (fleet intent) + app_registry (per-instance realisation), <root>/db/multiinstance.db",
			Engine:   EngineAppSync,
			Status:   StatusSyncs,
			Evidence: "backend/internal/multiinstance/appsync.go",
			Anchor:   "func (as *AppSync) DesireInstall(",
			Why: "A signed-changeset CRDT with uninstall quorum, roster verification and generation epochs was built and wired over the " +
				"fabric transport — and until 2026-08-16 it replicated a table NOTHING EVER WROTE. LocalInstall/LocalUninstall had no " +
				"non-test caller in 512 scanned files; POST /api/store/install went to AppStore.Install, which creates a directory. " +
				"Two things were missing and only one of them was wiring. The other was shape: app_registry is keyed " +
				"(instance_ulid, app_id), a per-instance INVENTORY, which records what a box has — a description. \"I installed Steam, put " +
				"it everywhere\" is an INTENT and no row could hold one. So app_desired (one row per app, no instance in the key) now " +
				"carries desire, app_registry carries realisation, and the handlers write both.",
			Consequence: "The replicated record now reflects real installs and real removals, and a box that cannot realise a desired app " +
				"reports why on a row the other boxes can read.",
			Note: "Removal is a TOMBSTONE, never a deleted row: an absence is indistinguishable from \"never wanted\", so any peer still " +
				"holding a pre-removal copy would resurrect the app on every sync, forever. Nothing in the realisation path writes " +
				"app_desired, so one broken box cannot uninstall the fleet. Pinned by TestInstalledAppSetHasBothProducers, which scans the " +
				"source rather than trusting this comment.",
		},
		{
			Name:     "app catalogue (which apps are installABLE)",
			Where:    "<root>/registry.json — a signed release artifact",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/services/appnet/store.go",
			Anchor:   "VULOS_REGISTRY",
			Why: "Identical on every box by construction rather than by replication: it ships with the release and is signed by " +
				"the release key. Replicating it would let one box's copy overwrite another's, which is a downgrade path, not a feature.",
		},
		{
			Name:     "per-app runtime data directory",
			Where:    "<root>/data/<appID>, symlinked to <root>/apps/<appID>/data",
			Engine:   EngineFileSync,
			Status:   StatusPartial,
			Evidence: "backend/services/sync/sync.go",
			Anchor:   `DataDir:           filepath.Join(vulos, "data")`,
			Why: "<root>/data IS watched by the file syncer and does cross, in both directions since the pull loop was wired. But " +
				"it only runs when the S3 cluster is configured and enabled, and it carries an app's data to a box that does not " +
				"have the app installed.",
			Consequence: "An app's saved data can arrive on a box that cannot open it, because the data syncs and the app does not.",
			Note:        "Gated on VULOS_S3_ACCESS_KEY + VULOS_CLUSTER_PASSPHRASE; absent those, nothing in this row moves.",
		},
		{
			Name:     "per-app sandbox storage (appfs)",
			Where:    "<root>/<userID>/<appID>/",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/services/appfs/appfs.go",
			Anchor:   "package appfs",
			Why: "Sits directly under the data root, NOT under <root>/data, so it falls outside the one directory the file syncer " +
				"watches. The distinction is invisible from the API an app uses — an app writing through /api/appdata gets " +
				"unsynced storage while an app writing to its data dir gets synced storage.",
			Consequence: "What an app saves for you is present on one box and absent on the other, with no rule a user could " +
				"predict, because it depends on which of two storage APIs the app's author happened to use.",
		},
		{
			Name:     "app launcher visibility and suite selection",
			Where:    "<root>/db/visibility.json, <root>/db/suite-selection.json",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/cmd/server/routes_suiteapps.go",
			Anchor:   "suite-selection.json",
			Why:      "Loose JSON in <root>/db, which no replicator watches: services/sync covers <root>/data and browser-profiles only.",
			Consequence: "Hide an app from your launcher on one box and it is still there on the other. The same is true of " +
				"choosing which suite apps you want.",
		},

		// ── how you configure and theme it ──────────────────────────────────
		{
			Name:     "user settings (theme, locale, timezone, AI provider, preferences)",
			Where:    "profiles table in <root>/db/auth.db",
			Engine:   EngineCRDT,
			Status:   StatusSyncs,
			Domain:   "sql:profiles",
			Evidence: "backend/internal/sqlcrdt/tables.go",
			Anchor:   `Name: "profiles"`,
			Why: "The flagship success of the current engine, and the one that took a storage-layer change to earn: credentials " +
				"were moved out into profile_secrets so the replicated row's bytes do not contain them.",
			Note: "LAN only, and only where the fabric layer runs (VULOS_LAN_ENABLE=1 — on in the shipped systemd unit, off for a bare process).",
		},
		{
			Name:     "theme, accent, night shift (the copy the shell actually uses)",
			Where:    "browser localStorage keys vulos-theme, vulos-accent, vulos-nightshift-*",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "frontend/src/core/ThemeProvider.tsx",
			Anchor:   "localStorage.setItem(key, val)",
			Why: "There are TWO themes. profiles.Theme is a replicated column that the shell's ThemeProvider neither reads nor " +
				"writes; the theme a user actually sees is a localStorage key. So the row that syncs is not the setting that " +
				"governs, and the setting that governs is per-browser — not even per-box.",
			Consequence: "Set your theme on one box and the other still opens in the old one. Open the SAME box in a different " +
				"browser and it is the old one there too.",
		},
		{
			Name:     "wallpaper",
			Where:    "browser localStorage key vulos-wallpaper",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "frontend/src/core/useWallpaper.tsx",
			Anchor:   "const STORAGE_KEY = 'vulos-wallpaper'",
			Why: "No backend endpoint for wallpaper exists at all — a grep for it across backend/ returns nothing. The state has " +
				"never left the browser, so there is nothing for a replicator to carry.",
			Consequence: "Your wallpaper is not a property of your OS. It is a property of the browser profile you last set it in.",
		},

		// ── how you arrange it ──────────────────────────────────────────────
		{
			Name:     "desktop layout, icon arrangement and dock profile",
			Where:    "browser localStorage keys vulos.desktop.layout, vulos.desktop.packs",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "frontend/src/desktop/store.ts",
			Anchor:   "function writeRaw(key: string, value: unknown): void",
			Why:      "Same class as wallpaper: persisted in the browser, with no server-side representation to replicate.",
			Consequence: "Arrange your desktop on one box and the other keeps the stock layout. The directive names 'arrange' " +
				"explicitly and this is the state it means.",
		},
		{
			Name:     "dock pins",
			Where:    "browser localStorage key vulos-dock-pins",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "frontend/src/shell/Dock.tsx",
			Anchor:   "const PIN_KEY = 'vulos-dock-pins'",
			Why: "Pinning is the single most deliberate arrangement act a user performs, and it is stored where nothing can reach it. " +
				"Note this is separable from window geometry: a pinned app is a CHOICE, whereas a window position is a fact about a screen.",
			Consequence: "Pin your six apps on the laptop box and the desktop box shows the preset's defaults.",
		},
		{
			Name:     "widget rail layout",
			Where:    "browser localStorage",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "frontend/src/widgets/layout.ts",
			Anchor:   "export function saveLayout(layout: WidgetLayout): void",
			Why: "Same class as the desktop layout: a localStorage blob with no server-side representation. Worth listing " +
				"separately because it is a different subsystem with its own key, and a fix for the desktop layout will not " +
				"incidentally carry it.",
			Consequence: "The widgets you chose, and the order you put them in, exist on one box only.",
		},
		{
			Name:     "window geometry, open windows and virtual desktops",
			Where:    "browser localStorage key vulos-shell-state",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "frontend/src/providers/ShellProvider.tsx",
			Anchor:   "export function saveShellState(state: ShellState): void",
			Why: "DECIDED, not inherited. A window rectangle is a statement about a particular screen: replicating a 2560x1440 " +
				"desktop layout onto a phone produces windows partly or wholly off-screen, and this OS explicitly targets phones as " +
				"thin clients to the same box. The right shape is per-DEVICE-CLASS geometry keyed off a synced identity, not one " +
				"global rectangle. Until that exists, not syncing is the correct behaviour rather than a missing feature — " +
				"which is exactly why it must not be lumped in with dock pins and wallpaper, which have no such argument.",
			Note: "Narrow exception: the GEOMETRY is excepted, the SET of open windows is not obviously so. Restoring which apps " +
				"were open is arguably desirable cross-device and is left as specified work rather than claimed as decided.",
		},

		// ── what you store ──────────────────────────────────────────────────
		{
			Name:     "Drive file metadata (the file tree, ACLs, shares, versions)",
			Where:    "files_nodes and 10 sibling tables in <root>/db/files.db",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/services/files/migrations/0001_initial.sql",
			Anchor:   "CREATE TABLE IF NOT EXISTS files_nodes",
			Why: "files.db appears nowhere in crdtsync/policy.go — not as an approval and not as a refusal. It is the largest body " +
				"of user-authored state in the OS and it has never been considered for replication, which under an allow-list means " +
				"it fails closed and stays put.",
			Consequence: "Your Drive is a different Drive on each box. A file you can see on one is not merely unopenable on the " +
				"other, it does not appear in the tree.",
			Note: "services/files/peershare.go can move an individual file a user explicitly shares. That is a share primitive, not " +
				"replication — it moves what you hand over, not what you own.",
		},
		{
			Name:     "Drive file bytes",
			Where:    "per-user object-store bucket, or <root>/storage on local disk (the default for an installer-produced box)",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/internal/storage/resolver.go",
			Anchor:   "LocalRoot",
			Why: "When an object store is configured the bytes are in a shared bucket and are reachable from both boxes; with the " +
				"DEFAULT local-disk resolver they are on one box's filesystem, outside every watched directory. So the honest status " +
				"is the default one, and the default does not cross.",
			Consequence: "On a stock box, the bytes of your files exist on exactly one machine.",
		},
		{
			Name:     "reminders",
			Where:    "reminders table in <root>/db/reminders.db",
			Engine:   EngineCRDT,
			Status:   StatusSyncs,
			Domain:   "sql:reminders",
			Evidence: "backend/internal/sqlcrdt/tables.go",
			Anchor:   "crdtsync.DomainReminders",
			Why:      "Replicated at column granularity, so marking one done on one box while editing its text on another keeps both edits.",
			Note:     "LAN only.",
		},
		{
			Name:     "notification history and Do Not Disturb",
			Where:    "<root>/db/notifications.json, <root>/db/dnd.json",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/services/notify/notify_store.go",
			Anchor:   "notifications.json",
			Why: "Loose JSON in <root>/db. Cross-instance fan-out DEDUP exists (seen_notifications in multiinstance.db) so the same " +
				"notification is not delivered twice — but the history and the DND state themselves stay put.",
			Consequence: "Dismiss a notification on one box and it is still waiting on the other. Turn on Do Not Disturb before bed " +
				"and the other box notifies anyway.",
		},
		{
			Name:     "contacts (unified view)",
			Where:    "nowhere — assembled in memory per request from lilmail CardDAV, pushed Android contacts and the box SIM",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/services/contacts/contacts.go",
			Anchor:   "package contacts",
			Why: "Derived state with no local authority. Replicating a projection of external sources would create a second, " +
				"staler authority for data whose real home is elsewhere. The sources are what would need to sync, not the view.",
		},
		{
			Name:     "calendar and mail",
			Where:    "not on the box — proxied to an external lilmail service",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/cmd/server/routes_pim.go",
			Anchor:   "package main",
			Why: "There is no local store to replicate: both boxes are clients of the same remote service and therefore already " +
				"see the same data. This is an exception in the strongest sense — the state is identical without any engine.",
		},

		// ── who you are ─────────────────────────────────────────────────────
		{
			Name:     "user account and password hash",
			Where:    "users table in <root>/db/auth.db",
			Engine:   EngineCRDT,
			Status:   StatusSyncs,
			Domain:   "sql:users",
			Evidence: "backend/internal/sqlcrdt/tables.go",
			Anchor:   `Name: "users"`,
			Why: "The hash replicates deliberately: withholding it leaves the account present on the second box and impossible to " +
				"log into, which pushes people into a second hand-made account with a weaker password. Requires a reload hook, " +
				"because auth.Store caches its working set in memory.",
			Note: "LAN only.",
		},
		{
			Name:     "login sessions",
			Where:    "sessions table in <root>/db/auth.db",
			Engine:   EngineNone,
			Status:   StatusException,
			Domain:   "sql:sessions",
			Evidence: "backend/internal/crdtsync/policy.go",
			Anchor:   `Domain: "sql:sessions"`,
			Why: "Per-device authentication state. A bearer token is usable directly, so replicating one multiplies the blast " +
				"radius of any single compromised box, and revoking on one box could not be relied on to revoke elsewhere.",
		},
		{
			Name:     "profile secrets (AI API key, device PIN hash)",
			Where:    "profile_secrets table in <root>/db/auth.db",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/services/auth/migrations/0002_profile_secrets.sql",
			Anchor:   "profile_secrets",
			Why: "This table exists PRECISELY so that profiles can replicate. A PIN belongs to a machine someone is standing at; " +
				"an API key is a bearer secret that can be spent. Splitting them out is what made the settings row safe to carry.",
		},
		{
			Name:     "security audit trail",
			Where:    "acctsec_sensitive_actions in <root>/db/accountsecurity.db",
			Engine:   EngineCRDT,
			Status:   StatusSyncs,
			Domain:   "sql:acctsec_sensitive_actions",
			Evidence: "backend/internal/sqlcrdt/tables.go",
			Anchor:   "acctsec_sensitive_actions",
			Why: "Grow-only, which is what makes it safe: an attacker who erases the local log no longer erases the evidence, " +
				"because merge keeps the first writer and tombstones are refused.",
			Note: "LAN only.",
		},

		// ── secrets and keys ────────────────────────────────────────────────
		{
			Name:     "password manager vault",
			Where:    "<root>/auth/vault/<userID>/vault.enc and vault.key",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/services/credvault/vault.go",
			Anchor:   "vault.enc",
			Why: "This is the one gap that is NOT resolved by declaring it an exception. A password manager that exists on one " +
				"machine is a password manager people stop using. The contents SHOULD follow the user; what must not follow is " +
				"vault.key, which is the per-device wrapping key. The correct shape is a replicated ciphertext with per-device " +
				"key wrapping — the same shape eviction re-keying needs — not a refusal.",
			Consequence: "Save a password on one box and it is not there on the other. Worse: <root>/auth is covered by NO backup " +
				"path (services/sync watches <root>/data; the restic driver backs up <root>/data), so losing that one box loses " +
				"every stored password permanently.",
		},
		{
			Name:     "TOTP keychain and passkeys",
			Where:    "<root>/auth/totp/<userID>/, <root>/auth/passkeys/<userID>/",
			Engine:   EngineNone,
			Status:   StatusPartial,
			Evidence: "backend/services/passkeys/passkeys.go",
			Anchor:   "package passkeys",
			Why: "Split verdict, and the split is the point. A PASSKEY is bound to the authenticator that holds it and must not " +
				"leave the device — a syncing passkey is a security defect. A TOTP SEED is an ordinary shared secret with no device " +
				"binding, and a second-factor app that only works at one desk is the same failure as the password vault.",
			Consequence: "Your authenticator codes are on one box only, and like the vault they sit in <root>/auth, which no " +
				"backup path covers.",
		},
		{
			Name:     "recovery blobs, master key envelopes and API key hashes",
			Where:    "<root>/db/auth.db, <root>/db/publicapi_keys.db",
			Engine:   EngineNone,
			Status:   StatusException,
			Domain:   "sql:recovery_blobs",
			Evidence: "backend/internal/crdtsync/policy.go",
			Anchor:   `Domain: "sql:recovery_blobs"`,
			Why:      "The entire point of an enveloped key is that it exists in few places. Copying it to every box defeats the envelope.",
		},
		{
			Name:     "device key and its revocation record",
			Where:    "<root>/auth/tpm/device_key.priv (or a TPM), revocations.json",
			Engine:   EngineRevSync,
			Status:   StatusException,
			Evidence: "backend/services/devicekey/revocation.go",
			Anchor:   "package devicekey",
			Why: "The KEY must never sync — a device key that appears on two machines stops being a device key, and this product's " +
				"threat model has boxes vouching for one another on the strength of it. The REVOCATION record is the opposite case " +
				"and does replicate, through a purpose-built pull loop: a revocation is only useful if every box hears it.",
			Note: "The asymmetry is the correct one, and it is the closest thing in the codebase to the eviction primitive the " +
				"second directive asks for.",
		},
		{
			Name:     "fabric shared secret (VULOS_FABRIC_SECRET)",
			Where:    "environment variable, identical on every box in the set",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/internal/multiinstance/appsync.go",
			Anchor:   "VULOS_FABRIC_SECRET",
			Why: "A single group bearer secret. It is an exception from replication only because it is distributed out of band — " +
				"and that is a WEAKNESS, not a design: because it is identical everywhere it cannot be revoked for one box without " +
				"re-keying all of them, and there is no re-key path. See roadmap/SYNC-INVENTORY.md's eviction section.",
		},
		{
			Name:     "per-instance signing key and instance identity",
			Where:    "<root>/data/fabric_instance_key, <root>/db/instance.json, <root>/peering/identity/ed25519.priv",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/internal/multiinstance/instancekey.go",
			Anchor:   "package multiinstance",
			Why: "An instance's identity is the one thing that must differ between instances, or the fleet cannot tell its members " +
				"apart — which is exactly what quorum, attribution and eviction all depend on. The strongest exception in this table.",
			Note: "Note the path: fabric_instance_key sits UNDER <root>/data, which the file syncer watches. It is sealed under " +
				"VULOS_FABRIC_KEY_HEX, so what would cross is ciphertext, but a per-instance key living inside a replicated " +
				"directory is a placement worth reviewing rather than relying on the seal.",
		},

		// ── this machine's own hardware and topology ────────────────────────
		{
			Name:     "storage mode and cgroup slices",
			Where:    "<root>/db/storagemode.db, cgroup_slices",
			Engine:   EngineNone,
			Status:   StatusException,
			Domain:   "sql:storagemode",
			Evidence: "backend/internal/crdtsync/policy.go",
			Anchor:   `Domain: "sql:storagemode"`,
			Why: "A description of hardware the other box does not have. Replicating one box's storage mode or CPU/memory limits " +
				"onto a different machine is a correctness error, not a privacy one.",
		},
		{
			Name:     "network mode, relay config, TURN credentials, gateway URL",
			Where:    "<root>/db/network-mode.json, relayconfig.json, turn.json, gateway.json",
			Engine:   EngineNone,
			Status:   StatusPartial,
			Evidence: "backend/services/relayconfig/relayconfig.go",
			Anchor:   "relayconfig.json",
			Why: "Mixed, and currently treated as uniformly local. A box's own listen addresses and interface state are genuinely " +
				"per-node. But which relay a user's fleet uses, and the TURN credentials for it, are a property of the FLEET and " +
				"having to configure them once per box is the kind of divergence the directive exists to prevent.",
			Consequence: "Point one box at your relay and the others still do not know about it.",
		},
		{
			Name:     "WiFi credentials",
			Where:    "/etc/wpa_supplicant/wpa_supplicant.conf — outside Vulos state entirely",
			Engine:   EngineNone,
			Status:   StatusException,
			Evidence: "backend/services/wifi/wifi.go",
			Anchor:   "wpa_supplicant",
			Why: "Written straight to the system supplicant, with no Vulos-side record to replicate. Also correct on the merits: " +
				"the networks in range differ per location, and boxes are frequently wired.",
		},
		{
			Name:     "browser profiles",
			Where:    "<root>/db/browser-profiles/",
			Engine:   EngineFileSync,
			Status:   StatusSyncs,
			Evidence: "backend/services/sync/sync.go",
			Anchor:   "BrowserProfileDir",
			Why:      "One of exactly two directories the file syncer watches.",
			Note:     "Requires the S3 cluster to be configured and enabled; otherwise nothing moves.",
		},
		{
			Name:     "durable backup of the databases",
			Where:    "S3 object cluster/snapshot/<version>.db.enc",
			Engine:   EngineSnapshot,
			Status:   StatusPartial,
			Evidence: "backend/services/sync/dbio.go",
			Anchor:   "func SnapshotDB(dbPath string) DBSnapshot",
			Why: "The cold path snapshots ONE database file, defaulting to auth.db, and only when VULOS_BACKUP_INTERVAL is set — " +
				"off by default. reminders.db, files.db, accountsecurity.db, every loose JSON file in <root>/db, and all of " +
				"<root>/auth are outside it.",
			Consequence: "A box restored from the durable backup comes back with accounts and settings and without reminders, " +
				"Drive, audit history, or any stored password.",
		},
		{
			Name:     "joining a cluster from a new device",
			Where:    "<root>/db/sync-state.json, driven by services/joinsync",
			Engine:   EngineNone,
			Status:   StatusGap,
			Evidence: "backend/services/joinsync/backend.go",
			Anchor:   "noopInstall",
			Why: "The join pull DOWNLOADS the snapshot, confirms it decrypts, and then discards it: the DBInstaller and " +
				"ChangesetApplier handed to Bootstrap are both no-ops. Nothing applies the data. The deferral comment says the " +
				"live services/sync engine will apply it at boot, and that is not so — the only Restorer call sites are an admin " +
				"HTTP endpoint and a CLI subcommand, neither of which runs on the boot path.",
			Consequence: "Join a new box to your cluster, watch the wizard report 100% complete, and find an empty machine. The " +
				"progress bar is measuring a readability check.",
		},
	}
}

// GapsInInventory returns the entries the directive says should sync and that
// do not. Used by the guard's coverage assertion and by anyone who wants the
// defect list without re-deriving it from the table.
func GapsInInventory() []StateEntry {
	var out []StateEntry
	for _, e := range OSStateInventory() {
		if e.Status == StatusGap || e.Status == StatusPartial {
			out = append(out, e)
		}
	}
	return out
}
