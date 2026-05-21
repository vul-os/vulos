# Netboot — iPXE stick + UEFI HTTP Boot (NETB-01)

Two entry paths both converge on the same chainload: fetch a signed kernel +
initramfs + squashfs over the network and boot it into a live-RAM session.

```
UEFI HTTP Boot   ─┐
                  ├──► chainload boot.vulos.org ──► kernel + initramfs + squashfs ──► live-RAM
~1 MB iPXE stick ─┘
```

See [roadmap/NETBOOT.md](../../roadmap/NETBOOT.md) for the full boot pipeline,
install flow, and two-layer safety model.

---

## Files

| File | Purpose |
|---|---|
| `boot.ipxe` | iPXE script embedded into the stick image; does DHCP + chainload |
| `build-ipxe-stick.sh` | Builds the ~1 MB USB stick image (tool-guarded) |
| `README.md` | This file |

---

## Path 1: ~1 MB one-time iPXE USB stick

### What it does

1. Machine powers on, firmware finds the USB stick.
2. iPXE runs `boot.ipxe`:
   - DHCP lease
   - Tries `https://boot.vulos.org/boot.ipxe` (iPXE TLS — opportunistic)
   - Falls back to `http://boot.vulos.org/boot.ipxe` on TLS failure
3. Remote script serves the kernel + initramfs + squashfs for the live-RAM session.
4. User clicks **Install** to write the seed to local disk.
5. The stick is never needed again — steady-state boots are local.

### Build the stick image

```sh
# From the repo root — calls the build-ipxe-stick.sh helper:
./build.sh --netboot-stick

# Or directly:
scripts/netboot/build-ipxe-stick.sh [--outdir output/]
```

**Output:** `output/vulos-netboot-stick.img` (~1 MB)

### Flash

```sh
dd if=output/vulos-netboot-stick.img of=/dev/sdX bs=512 status=progress
```

### Toolchain (iPXE build dependencies)

The build script tool-guards the iPXE toolchain. When the toolchain is absent
it prints the manual `make` commands and exits without error.

Install on Debian/Ubuntu:

```sh
apt-get install make gcc binutils liblzma-dev perl mtools xz-utils
```

Manual build (equivalent to what the script does):

```sh
git clone --depth=1 https://github.com/ipxe/ipxe.git /tmp/ipxe
cd /tmp/ipxe/src

# Legacy BIOS USB stick (~1 MB):
make bin/ipxe.usb EMBED=/path/to/scripts/netboot/boot.ipxe

# UEFI USB stick (x86_64):
make bin-x86_64-efi/ipxe.usb EMBED=/path/to/scripts/netboot/boot.ipxe

cp bin/ipxe.usb output/vulos-netboot-stick.img
```

Set `VULOS_IPXE_SRC=/path/to/ipxe/src` to use an existing source tree instead
of letting the script clone a fresh copy.

---

## Path 2: UEFI HTTP Boot (no media)

Modern UEFI firmware (UEFI 2.5+, OVMF, most Intel/AMD boards since ~2018) can
boot directly from an HTTP URL without any USB stick.

### URL to configure

```
http://boot.vulos.org/boot.ipxe
```

**Why plain HTTP?** UEFI HTTP Boot implementations vary in their HTTPS/TLS
support. Plain HTTP ensures broad compatibility. Security is not provided by
the transport — it is provided by **artifact signatures** (see below).

### How to set the UEFI HTTP Boot URL

**System firmware (BIOS setup):**

1. Enter BIOS/UEFI setup (usually F2, F10, Del, or Esc at POST).
2. Navigate to: *Boot → Add Boot Option* (name varies by vendor).
3. Select **HTTP** as the boot type.
4. Enter the URL: `http://boot.vulos.org/boot.ipxe`
5. Save and reboot.

**UEFI shell:**

```
BcfgAdd boot 0 http://boot.vulos.org/boot.ipxe "Vulos OS Netboot"
```

**EDK2 / OVMF (QEMU):**

```sh
qemu-system-x86_64 \
  -bios /usr/share/ovmf/OVMF.fd \
  -netdev user,id=net0,tftp=…,bootfile=http://boot.vulos.org/boot.ipxe \
  -device virtio-net-pci,netdev=net0
```

### What happens

1. Firmware performs DHCP.
2. Firmware fetches `http://boot.vulos.org/boot.ipxe` as a UEFI HTTP Boot
   image (EFI application or iPXE binary served with the appropriate MIME type).
3. iPXE / the boot script serves kernel + initramfs + squashfs.
4. Live-RAM session starts; user can install.

---

## Security model

Both paths share the same two-layer safety design (roadmap/NETBOOT.md):

| Layer | Protects | Mechanism |
|---|---|---|
| **Artifact signatures** | payload integrity (tampered images) | every fetched artifact is verified against the baked trust anchor (SEED-01/SIGNING.md) |
| **TLS (opportunistic)** | transport confidentiality / MITM | iPXE TLS used when available; plain HTTP accepted when not |

**NETB-01** (this task) produces the stick + script.  
**NETB-02** (future) adds `imgverify` + Secure Boot shim integration.

Neither TLS alone nor signatures alone are sufficient; together they cover
transport and content. Plain HTTP is acceptable *only because* every artifact
is signature-verified before execution — a compromised CDN or MITM attacker
cannot get unsigned code executed.

---

## Self-hosted boot server

The boot URL is configurable. Any server that serves the signed artifacts works:

```sh
# Override at boot.ipxe build time — edit boot.ipxe before embedding:
set BOOT_URL_HTTPS https://boot.my-fork.example.com/boot.ipxe
set BOOT_URL_HTTP  http://boot.my-fork.example.com/boot.ipxe
```

The trust anchor baked into the image (SEED-01) must match the signing key
used to sign the artifacts on your server. See [roadmap/SEED-TRUST.md](../../roadmap/SEED-TRUST.md)
for the fork procedure.
