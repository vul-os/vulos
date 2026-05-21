# Secure Boot Shim Signing Plan (NETB-02)

Secure Boot is the firmware-level anchor for the signed boot chain on UEFI
systems. The shim is the first Vulos-controlled binary that firmware loads; it
validates everything that follows (bootloader → kernel → initramfs). Two paths
are supported — choose based on deployment type.

---

## Why a shim?

UEFI Secure Boot firmware only launches EFI binaries whose signatures chain up
to a key in the firmware's **DB** (allowed) list. Most consumer hardware ships
with the **Microsoft UEFI CA** pre-enrolled. Rather than requiring every user to
enroll a custom key (which requires physical presence at a BIOS prompt), the
standard approach is:

1. Use the upstream **shim** (a Microsoft-signed EFI stub).
2. Shim verifies your bootloader against **your own embedded certificate**.
3. Your bootloader verifies your kernel.

This keeps the Vulos-specific keys entirely under Vulos control while still
being accepted by factory-default UEFI firmware.

---

## Path A: Microsoft UEFI CA (shim-review) — recommended for distribution

This path requires no user interaction and works out-of-the-box on all
major x86 hardware.

### Steps

1. **Build the shim binary** with the Vulos second-stage certificate embedded.

   ```sh
   # Clone the upstream shim source (https://github.com/rhboot/shim):
   git clone https://github.com/rhboot/shim.git
   cd shim

   # Build with the Vulos vendor certificate embedded:
   #   VENDOR_CERT_FILE — PEM certificate that shim will use to verify GRUB/iPXE.
   make VENDOR_CERT_FILE=/path/to/keys/vulos-vendor.cer
   ```

2. **Submit to shim-review** — open a GitHub issue in
   [rhboot/shim-review](https://github.com/rhboot/shim-review) with:
   - Organisation name: Vulos
   - Shim binary SHA-256 + source URL
   - Vendor certificate hash
   - Description of what the shim will boot
   - Answer all checklist questions in the issue template

3. **Microsoft signs the shim.** After shim-review approval Microsoft signs
   the submitted binary via their UEFI CA. This produces a
   `shimx64.efi`/`shimia32.efi` that any stock UEFI Secure Boot hardware
   will launch without additional key enrollment.

4. **Package the signed shim** into the Vulos seed (SEED-01):

   ```
   EFI/BOOT/
   ├── BOOTx64.EFI         ← Microsoft-signed shimx64.efi (renamed)
   ├── grubx64.efi         ← GRUB2 signed with vulos-vendor key
   └── vulos-vendor.cer    ← Vulos vendor certificate (embedded in shim)
   ```

5. **Sign GRUB and the kernel** with the Vulos vendor private key:

   ```sh
   sbsign --key keys/vulos-vendor.key \
          --cert keys/vulos-vendor.cer \
          --output grubx64.efi grubx64.unsigned.efi

   sbsign --key keys/vulos-vendor.key \
          --cert keys/vulos-vendor.cer \
          --output vmlinuz-signed vmlinuz
   ```

### Key hierarchy

```
Microsoft UEFI CA (in firmware DB)
  └── shimx64.efi (Microsoft-signed)
        └── vulos-vendor.cer (embedded in shim)
              ├── grubx64.efi (Vulos-signed)
              │     └── vmlinuz (Vulos-signed kernel)
              └── iPXE EFI binary (optional; Vulos-signed)
```

### Timelines and considerations

- shim-review approval typically takes 4–12 weeks.
- A new shim submission is required whenever the vendor certificate changes.
- Keep the vendor private key offline (HSM or air-gapped machine).
- The vendor certificate should have a short validity period (2–3 years) and
  must be renewed before expiry with a new shim submission.

---

## Path B: Self-enrolled key via mokutil — managed/enterprise fleets

This path is suitable for organisations that control their own hardware and
can enroll keys via BIOS setup or `mokutil` pre-enrollment. No shim-review
required; no Microsoft involvement. Not practical for end-user distribution.

### Steps

1. **Generate a Machine Owner Key (MOK) pair:**

   ```sh
   openssl req -newkey rsa:2048 -nodes \
     -keyout keys/vulos-mok.key \
     -new -x509 -sha256 \
     -days 3650 \
     -subj "/CN=Vulos MOK/" \
     -out keys/vulos-mok.cer
   ```

2. **Enroll the key on each target machine** using one of:

   a. **mokutil** (Linux, requires a reboot + physical confirmation):
   ```sh
   sudo mokutil --import keys/vulos-mok.cer
   # Reboot → MOK Manager UI → Enroll MOK → provide password set above
   ```

   b. **BIOS/UEFI setup** (no OS required):
   - Enter firmware setup (F2/Del/F10).
   - Navigate to *Secure Boot → Authorized Signatures (DB)* or *MOK Management*.
   - Import `vulos-mok.cer`.

   c. **Pre-enrollment via provisioning** (PXE/automation):
   - Use `mokutil --import` in a pre-image step.
   - Or use UEFI vendor tools (`efi-updatevar`, vendor BIOS scripting) to
     inject the certificate into the DB directly.

3. **Sign EFI binaries** with the MOK:

   ```sh
   sbsign --key keys/vulos-mok.key \
          --cert keys/vulos-mok.cer \
          --output shimx64.efi shimx64.unsigned.efi

   sbsign --key keys/vulos-mok.key \
          --cert keys/vulos-mok.cer \
          --output vmlinuz-signed vmlinuz
   ```

4. **No shim modification required.** The upstream shim (without Vulos
   modifications) will respect the enrolled MOK key from the MokList.

### Key hierarchy (self-enrolled)

```
Vulos MOK certificate (enrolled in firmware DB or MokList)
  ├── shimx64.efi (MOK-signed)  → optional, can use unsigned shim if MOK is in DB
  ├── grubx64.efi (MOK-signed)
  └── vmlinuz (MOK-signed)
```

### When to use Path B

- Internal fleet deployments where you control the hardware provisioning.
- Development / CI environments.
- Air-gapped or high-security environments where external CA involvement is
  undesirable.

---

## iPXE and Secure Boot

When booting via the ~1 MB iPXE stick (NETB-01/NETB-02) on a Secure Boot
machine, the iPXE EFI binary must itself be Secure Boot-signed:

```sh
# Sign the iPXE UEFI binary with the Vulos vendor/MOK key:
sbsign --key keys/vulos-vendor.key \
       --cert keys/vulos-vendor.cer \
       --output ipxe-signed.efi ipxe.efi
```

The signed iPXE binary is then embedded into the stick image as `BOOTx64.EFI`
(or chainloaded from GRUB). iPXE's `imgverify` then handles signature
verification of every subsequent artifact as described in README.md.

**Secure Boot + imgverify = two independent defence layers:**

| Layer | What it verifies | When |
|-------|-----------------|------|
| Secure Boot (shim) | the iPXE EFI binary itself | at firmware load time |
| imgverify (iPXE) | every fetched artifact (manifest, kernel, initramfs) | at runtime, before exec |

Both layers must pass; either alone is insufficient.

---

## Key management summary

| Key | Use | Storage |
|-----|-----|---------|
| `vulos-vendor.key` (Path A) | Sign shim-loadable binaries (GRUB, kernel, iPXE) | Offline / HSM |
| `vulos-mok.key` (Path B) | Sign binaries for self-enrolled fleets | Offline / HSM |
| `trust-anchor` (NETB-02) | iPXE imgverify of boot artifacts | Offline / HSM |
| Release key (SIGN-03) | Sign `boot-pipe.json` + artifact `.sig` files | Offline / HSM |

The release key (SIGN-03) and the trust anchor are distinct: the release key
is an **online** key that signs each release's artifacts; the trust anchor is
the **baked root** that verifies them at boot time. The release key must be
rotated more frequently; the trust anchor requires a new stick image to change.

---

## References

- [rhboot/shim](https://github.com/rhboot/shim) — upstream shim source
- [rhboot/shim-review](https://github.com/rhboot/shim-review) — Microsoft CA signing process
- [mokutil(1)](https://linux.die.net/man/1/mokutil) — Machine Owner Key management
- [sbsign(1)](https://manpages.ubuntu.com/manpages/focal/man1/sbsign.1.html) — EFI binary signing
- [UEFI Specification §32](https://uefi.org/sites/default/files/resources/UEFI_Spec_2_10_Aug29.pdf) — Secure Boot
- [roadmap/SIGNING.md](../../roadmap/SIGNING.md) — Vulos signing design
- [roadmap/SEED-TRUST.md](../../roadmap/SEED-TRUST.md) — trust anchor baking + seed
