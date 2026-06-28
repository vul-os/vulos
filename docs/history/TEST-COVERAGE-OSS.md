# TEST-COVERAGE-OSS

Per-package Go test coverage for Vulos OSS backend.

Generated with: `cd backend && go test -cover ./...`  
Date: 2026-05-21  
Branch: audit/OSS-TEST-SUITE

## Per-package coverage

| Package | Coverage | Notes |
|---|---|---|
| `cmd/server` | 0.5% | Main binary wiring; low by design (integration tested elsewhere) |
| `cmd/sign` | 24.9% | CLI tool |
| `cmd/verify` | 75.3% | CLI tool |
| `firstboot` | (no stmts) | Integration test package; exercises joinsync + identity + bootmode |
| `integration` | (no stmts) | Integration test package |
| `internal/config` | 0.0% | Config loader; no unit tests yet |
| `internal/storage` | 0.0% | Storage helpers; no unit tests yet |
| `internal/ulid` | 0.0% | ULID utilities; no unit tests yet |
| `internal/vecdb` | 0.0% | Vector DB; no unit tests yet |
| `internal/wsutil` | 0.0% | WebSocket utilities; no unit tests yet |
| `security` | (no stmts) | Security test package |
| `services/ai` | 75.9% | |
| `services/appfs` | 68.7% | |
| `services/appnet` | 24.0% | Low: exec/flatpak/namespace paths require OS-level mocking |
| `services/audio` | 26.4% | Low: ALSA/PulseAudio paths untestable in unit suite |
| `services/auth` | 42.9% | +2.5pp from new auth_unit_test.go (previously 40.4%) |
| `services/authvault` | 64.6% | |
| `services/bluetooth` | 31.7% | Low: Bluetooth D-Bus paths |
| `services/bootmode` | 60.0% | |
| `services/clientcerts` | 71.1% | |
| `services/cluster` | 42.6% | |
| `services/concurrency` | 100.0% | |
| `services/credvault` | 71.7% | |
| `services/desktop` | 60.0% | |
| `services/devicekey` | 43.6% | |
| `services/disks` | 0.0% | Low: requires real block device paths |
| `services/display` | 29.1% | Low: display/Wayland paths |
| `services/drivers` | 16.0% | Low: udev/sysfs paths |
| `services/embeddings` | 21.8% | Low: embeddings model paths |
| `services/energy` | 44.7% | |
| `services/gateway` | 50.3% | |
| `services/gpu` | 9.5% | Low: GPU driver inspection paths |
| `services/identity` | 84.9% | |
| `services/input` | 5.8% | Low: evdev/uinput paths |
| `services/installer` | 69.7% | |
| `services/joincode` | 82.8% | |
| `services/joinsync` | 55.9% | |
| `services/kitbackup` | 96.3% | |
| `services/lease` | 69.8% | |
| `services/naming` | 92.3% | |
| `services/network` | 61.5% | |
| `services/notify` | 73.1% | |
| `services/osdist` | 69.5% | |
| `services/packages` | 0.0% | Low: apt/dpkg paths |
| `services/passkeys` | 80.9% | |
| `services/peering` | 74.2% | |
| `services/peering/sfu` | 63.5% | |
| `services/profiles` | 90.4% | |
| `services/pty` | 56.8% | |
| `services/recall` | 53.5% | |
| `services/sandbox` | 45.8% | |
| `services/signing` | 76.0% | |
| `services/smsotp` | 78.4% | |
| `services/sshkey` | 80.2% | |
| `services/storageprov` | 53.7% | |
| `services/store` | 59.0% | |
| `services/stream` | 1.3% | Low: WebRTC stream paths |
| `services/sync` | 65.6% | |
| `services/sysuser` | 10.0% | Low: Linux sysuser/PAM paths |
| `services/telemetry` | 11.7% | Low: OS-level telemetry collection |
| `services/telephony` | 70.0% | |
| `services/vault` | 33.9% | |
| `services/webbrowser` | 3.3% | Low: browser launch paths |
| `services/webproxy` | 49.1% | |
| `services/wifi` | 17.9% | Low: wpa_supplicant/NetworkManager paths |
| `services/wine` | 2.2% | Low: Wine binary paths |
| `services/wltoplevel` | 85.4% | |

## E2E coverage (tagged suite)

Run with `go test -tags=e2e ./backend/firstboot/e2e/...`

| Test | Scenario |
|---|---|
| `TestE2E_BmInit_FreshInstanceIsSetupMode` | bare-metal init → bootmode=setup |
| `TestE2E_FirstBootWizard_LocalAccount` | wizard local path → bootmode=normal |
| `TestE2E_CreateAccount_TwoUsersSeeded` | 2 seeded accounts alice+bob, role check |
| `TestE2E_JoinCluster_SeedPassphraseSetsNormal` | join with correct passphrase → normal |
| `TestE2E_JoinCluster_WrongPassphraseRejected` | wrong passphrase → state unchanged |
| `TestE2E_JoinCluster_AlreadyProvisionedBlocked` | provisioned guard (SECAUDIT2 L-2) |
| `TestE2E_PeerContacts_ThreeSeeded` | 3 seeded contacts: approved/pending/blocked |
| `TestE2E_PeerMessage_TwoSeedMessagesInInbox` | 2 seeded messages in inbox |
| `TestE2E_OTAStage_TrustAnchorSeeded` | OTA anchor key sign+verify round-trip |
| `TestE2E_OTAStage_BrokerPubkeySeeded` | cloud broker pubkey on disk |
| `TestE2E_Reboot_BootmodeTransitionsAfterJoin` | full lifecycle: setup→sync→normal |
| `TestE2E_ClusterHealth_DisabledWhenNotConfigured` | unconfigured cluster is disabled |
| `TestE2E_SeedPassphrase_NeverOnDisk` | passphrase security invariant |
| `TestE2E_IdentityPersistsAcrossReload` | identity.Load idempotent |

## Low-coverage packages — root cause analysis

Packages with < 20% coverage have uncovered code that falls into three categories:

1. **OS-level system calls** (`disks`, `drivers`, `input`, `gpu`, `sysuser`, `wifi`,
   `bluetooth`, `display`, `audio`, `wine`, `webbrowser`): These packages wrap
   Linux-specific interfaces (udev, evdev, wpa_supplicant, ALSA, D-Bus, etc.) that
   are not available on macOS CI and require privileged access or real hardware.
   Coverage improvement requires either Docker-based integration tests or extensive
   mocking of syscall boundaries.

2. **Package-manager paths** (`packages`, `appnet`): These call `apt`, `dpkg`,
   `flatpak`, and container runtimes. Mocking at the `exec.Cmd` level is possible
   but requires significant harness work.

3. **Stream / WebRTC** (`stream`, `embeddings`, `telemetry`): These involve
   media codecs, GPU paths, and WebRTC peer connections that need browser/WebRTC
   stubs.

The `internal/*` packages (config, storage, ulid, vecdb, wsutil) have 0%
because they are utility packages exercised indirectly by the packages that
import them; they are visible in coverage when `-coverpkg=./...` is used.
