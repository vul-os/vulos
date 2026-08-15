#!/usr/bin/env python3
"""arch-catalogue-audit.py — measure the real size of the x86_64-only problem.

Reads the ~120 Flatpak app ids out of roadmap/APP-CATALOG.md and asks Flathub
(https://flathub.org/api/v2/summary/<id>) which architectures each app is
ACTUALLY published for, plus whether it is an extra-data package (a thin
manifest that downloads a vendor binary at install time).

This exists because the catalogue's arch policy is stated in prose and
registry.json's `arch` field is unpopulated for every Flatpak entry — so the
size of the "cannot run on ARM" set was an assumption, not a measurement.

Output: a table + the counts, and a JSON blob for downstream use.
Usage: scripts/arch-catalogue-audit.py [--json out.json]
"""
import json
import re
import sys
import urllib.request
import urllib.error
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

CATALOG = Path(__file__).resolve().parent.parent / "roadmap" / "APP-CATALOG.md"
API = "https://flathub.org/api/v2/summary/{}"

# A Flatpak app id: reverse-DNS, at least three dot-separated segments.
# The trailing group captures the catalogue's flag markers (✓ ↻ + P X ?) that
# follow the id, so the `P` (proprietary) flag can be read rather than guessed.
ID_RE = re.compile(
    r"`([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+){2,})`([^`\n]*)"
)


def extract_ids(text: str) -> list[tuple[str, bool]]:
    """Return (app_id, is_proprietary) for every app in the catalogue list.

    Proprietary is read from the catalogue's own `P` marker (legend at the top
    of the list) rather than judged here — policy 1a excludes exactly the set
    the catalogue marks, so any other source of truth would drift from it.
    """
    # Only the catalogue section, so file paths / package names elsewhere in the
    # doc cannot be mistaken for app ids.
    start = text.find("## The catalogue")
    end = text.find("### Vulos first-party")
    section = text[start:end] if start >= 0 else text
    seen, out = set(), []
    for m in ID_RE.finditer(section):
        i, trailer = m.group(1), m.group(2)
        # The trailer runs up to the next id; the flags belong to THIS id only
        # until the ' · ' separator.
        flags = trailer.split("·")[0]
        prop = bool(re.search(r"(?<![A-Za-z])P(?![A-Za-z])", flags))
        if i not in seen:
            seen.add(i)
            out.append((i, prop))
    return out


def query(app_id: str) -> dict:
    try:
        with urllib.request.urlopen(API.format(app_id), timeout=25) as r:
            d = json.load(r)
    except urllib.error.HTTPError as e:
        return {"id": app_id, "error": f"HTTP {e.code}"}
    except Exception as e:  # noqa: BLE001 - network shape varies
        return {"id": app_id, "error": type(e).__name__}
    md = d.get("metadata") or {}
    return {
        "id": app_id,
        "arches": sorted(d.get("arches") or []),
        "extra_data": "extra-data" in md,
        "runtime": md.get("runtimeName", ""),
        "installed_size": d.get("installed_size"),
    }


def main() -> int:
    entries = extract_ids(CATALOG.read_text())
    prop_map = dict(entries)
    ids = [i for i, _ in entries]
    print(f"app ids found in catalogue: {len(ids)} "
          f"({sum(prop_map.values())} marked proprietary)", file=sys.stderr)
    with ThreadPoolExecutor(max_workers=8) as ex:
        rows = list(ex.map(query, ids))
    for r in rows:
        r["proprietary"] = prop_map.get(r["id"], False)

    both, x86only, armonly, errors = [], [], [], []
    for r in rows:
        if r.get("error"):
            errors.append(r)
        elif r["arches"] == ["aarch64", "x86_64"]:
            both.append(r)
        elif r["arches"] == ["x86_64"]:
            x86only.append(r)
        else:
            armonly.append(r)

    # Policy 1a (founder call, 2026-08-15): proprietary apps are excluded from
    # the catalogue for now. The number that matters for the "one OS across
    # mixed-arch instances" promise is therefore the x86_64-only set AFTER that
    # exclusion — the FOSS apps that genuinely cannot follow a user onto an ARM
    # instance.
    x86_free = [r for r in x86only if not r["proprietary"]]
    x86_prop = [r for r in x86only if r["proprietary"]]

    print(f"\n=== x86_64-ONLY *AND* NON-PROPRIETARY ({len(x86_free)}) "
          f"— THE ACTUAL PROBLEM SET ===")
    for r in sorted(x86_free, key=lambda r: r["id"]):
        xd = "  EXTRA-DATA(vendor binary)" if r["extra_data"] else ""
        print(f"  {r['id']}{xd}")

    print(f"\n=== x86_64-only but proprietary ({len(x86_prop)}) "
          f"— already out under policy 1a ===")
    for r in sorted(x86_prop, key=lambda r: r["id"]):
        print(f"  {r['id']}")

    print(f"\n=== both arches ({len(both)}) — sync uniformly, NO emulation needed ===")
    print("  " + ", ".join(sorted(r["id"] for r in both)))

    print(f"\n=== other/odd ({len(armonly)}) ===")
    for r in armonly:
        print(f"  {r['id']} {r['arches']}")

    print(f"\n=== not resolvable on Flathub ({len(errors)}) — WRONG ID OR NOT PUBLISHED ===")
    for r in errors:
        print(f"  {r['id']} {r['error']}")

    tot = len(rows)
    print(f"\nTOTALS: {tot} queried | both={len(both)} "
          f"x86_64-only={len(x86only)} other={len(armonly)} unresolvable={len(errors)}")
    resolvable = tot - len(errors)
    if resolvable:
        print(f"x86_64-only share of resolvable apps:        "
              f"{100.0*len(x86only)/resolvable:.1f}%  ({len(x86only)}/{resolvable})")
        print(f"x86_64-only AND non-proprietary share:       "
              f"{100.0*len(x86_free)/resolvable:.1f}%  ({len(x86_free)}/{resolvable})")
    xd = [r for r in x86only if r["extra_data"]]
    print(f"of the x86_64-only set, {len(xd)} are extra-data (vendor-downloaded binary)")

    if "--json" in sys.argv:
        out = sys.argv[sys.argv.index("--json") + 1]
        Path(out).write_text(json.dumps(rows, indent=2))
        print(f"wrote {out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
