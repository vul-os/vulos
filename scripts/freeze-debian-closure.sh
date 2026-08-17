#!/usr/bin/env bash
# freeze-debian-closure.sh — resolve a Debian dependency closure ONCE, at
# catalogue time, and freeze it into a pinned, per-package-checksummed manifest.
# Owned by roadmap/DISTRO-SOURCED-APPS.md.
#
# ── Why this exists ─────────────────────────────────────────────────────────
#
# roadmap/INSTALL-METHODOLOGY.md removed apt because `apt-get install -y blender`
# resolves to whatever Debian ships THAT DAY. Two instances a month apart get two
# different Blenders and neither the entry nor the sync wire records which, which
# is incompatible with "each instance is almost a direct clone of the next".
#
# The load-bearing word in that sentence is **unpinned**, not "apt". Nothing
# stops us running the solver ONCE, offline, when the catalogue entry is built,
# freezing the exact package set and every checksum, and shipping that frozen
# list as signed data. The box then does no solving at all: it fetches a fixed,
# verified list of files by exact URL and exact SHA-256 — which is precisely what
# vehicle B (per-arch `artifacts`) already does, with N payloads instead of one.
#
# ── The three rules this tool exists to keep ────────────────────────────────
#
#  1. **Every digest is computed from bytes that arrived.** apt's own
#     `--print-uris` emits an MD5 (and sometimes nothing at all — see the
#     `libexpat1` line in any trixie closure, which carries no hash field
#     because it comes from the security archive). We never copy that. Each
#     file is downloaded FROM THE URL THAT GOES IN THE MANIFEST and hashed
#     here. A digest that was not computed from the bytes the pinned URL serves
#     is a fabricated digest, and a fabricated digest is the worst defect this
#     catalogue has ever contained.
#
#  2. **The pinned URL is snapshot.debian.org, and it is verified by download,
#     not by reasoning.** deb.debian.org/pool/… is a MOVING target: the file
#     disappears the moment Debian publishes a new version of that package, so
#     a manifest built against it stops installing within weeks. snapshot is
#     immutable and archives every version ever published. But "the snapshot URL
#     I constructed by string substitution serves the same bytes" is a claim,
#     and this tool proves it by fetching from snapshot and hashing THAT.
#
#  3. **The closure is DIFFERENTIAL against a declared base, and the base is in
#     the manifest.** `apt-get install --print-uris` reports what is missing
#     from the container it runs in. Run it in `debian:trixie-slim` and you get
#     350 packages; run it in a container that already has python3 and curl and
#     you get 315. Neither number is wrong; they answer different questions.
#     The manifest therefore records the base image it was resolved against, and
#     a box whose base differs must NOT use the manifest. Leaving that implicit
#     is how a closure silently becomes wrong after an OS release.
#
# ── What the box does with the result — and why it is not a package manager ──
#
# The installer extracts each .deb with `dpkg-deb -x` into the APP'S OWN prefix
# (`$HOME/.vulos/apps/<id>/prefix`), never the system tree. On a live Vulos box
# the system tree is squashfs under dm-verity (read-only) and its overlay is a
# tmpfs, so writing there is both illegal and volatile.
#
# `dpkg-deb -x` is an ARCHIVE EXTRACTOR. A .deb is an `ar` archive containing a
# `data.tar.xz`; `-x` unpacks that tar and does nothing else. It consults no
# dpkg database, runs no maintainer script, resolves no dependency, and writes
# no state outside the target directory. That is the whole reason this is a
# legitimate second shape of vehicle B and not apt smuggled back in: **the
# solving happened here, on a curator's machine, months earlier, and its result
# is inside an Ed25519 signature.**
#
# Usage:
#   scripts/freeze-debian-closure.sh --app blender --package blender \
#       --arch arm64 [--arch amd64] [--suite trixie] \
#       [--base debian:trixie-slim] [--snapshot 20260816T000000Z] \
#       [--out registry.d/blender-debian.json]
#
#   scripts/freeze-debian-closure.sh --self-test    # no network, no docker
set -uo pipefail

APP=""; PKG=""; SUITE="trixie"; BASE="debian:trixie-slim"; SNAPSHOT=""; OUT=""
ARCHES=(); SELFTEST=0; RESOLVE_ONLY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --app)       APP="$2"; shift 2 ;;
    --package)   PKG="$2"; shift 2 ;;
    --arch)      ARCHES+=("$2"); shift 2 ;;
    --suite)     SUITE="$2"; shift 2 ;;
    --base)      BASE="$2"; shift 2 ;;
    --snapshot)  SNAPSHOT="$2"; shift 2 ;;
    --out)       OUT="$2"; shift 2 ;;
    --resolve-only) RESOLVE_ONLY=1; shift ;;   # closure SIZE only, no downloads
    --self-test) SELFTEST=1; shift ;;
    -h|--help)   sed -n '1,80p' "$0"; exit 0 ;;
    *) echo "freeze-debian-closure: unknown argument $1" >&2; exit 2 ;;
  esac
done

# ── validate_manifest — the checker, used by --self-test and by every write ──
validate_manifest() {
  python3 - "$1" <<'PY'
import json,re,sys
p=sys.argv[1]
try: m=json.load(open(p))
except Exception as e: print("CLOSURE-00 not JSON:",e); sys.exit(1)
errs=[]
if m.get("schema")!="vulos.debian-closure/1": errs.append("CLOSURE-13 unknown schema %r"%m.get("schema"))
if not m.get("base_image"): errs.append("CLOSURE-03 base_image is empty — a differential closure with no declared base is unusable")
snap=m.get("snapshot") or ""
if not re.fullmatch(r"\d{8}T\d{6}Z",snap): errs.append("CLOSURE-14 snapshot timestamp %r is not a snapshot.debian.org stamp"%snap)
arches=m.get("arches") or {}
if not arches: errs.append("CLOSURE-11 arches map is empty")
VULOS_ARCHES={"amd64","arm64","i386","armhf","riscv64","ppc64el","s390x"}
for a,blk in arches.items():
    if a not in VULOS_ARCHES:
        errs.append("CLOSURE-10 arch key %r is not a Debian/Vulos arch spelling (never aarch64/x86_64 here)"%a)
    pkgs=blk.get("packages") or []
    if not pkgs: errs.append("CLOSURE-15 %s has no packages"%a); continue
    if blk.get("package_count")!=len(pkgs):
        errs.append("CLOSURE-06 %s package_count=%r but the list holds %d"%(a,blk.get("package_count"),len(pkgs)))
    tot=sum(int(x.get("size") or 0) for x in pkgs)
    if blk.get("download_bytes")!=tot:
        errs.append("CLOSURE-07 %s download_bytes=%r but the sizes total %d"%(a,blk.get("download_bytes"),tot))
    seen={}
    for x in pkgs:
        fn=x.get("filename") or ""
        if not fn or fn.startswith("/") or ".." in fn.split("/"):
            errs.append("CLOSURE-12 %s filename %r escapes the download directory"%(a,fn))
        u=x.get("url") or ""
        if not u.startswith("https://snapshot.debian.org/archive/"):
            errs.append("CLOSURE-02/04 %s %s url is not an https snapshot.debian.org URL: %r — a pool URL on deb.debian.org is a MOVING target"%(a,fn,u))
        if snap and snap not in u:
            errs.append("CLOSURE-16 %s %s url is not pinned to the declared snapshot %s"%(a,fn,snap))
        h=(x.get("sha256") or "")
        if not re.fullmatch(r"[0-9a-f]{64}",h):
            errs.append("CLOSURE-01/09 %s %s sha256 is missing or not 64 hex chars: %r"%(a,fn,h))
        if int(x.get("size") or 0)<=0:
            errs.append("CLOSURE-08 %s %s size is %r"%(a,fn,x.get("size")))
        if h in seen and re.fullmatch(r"[0-9a-f]{64}",h):
            errs.append("CLOSURE-05 %s %s and %s share one sha256 — two different packages cannot have identical bytes"%(a,seen[h],fn))
        seen[h]=fn
if errs:
    for e in errs: print(e)
    sys.exit(1)
print("manifest OK")
PY
}

# ── The self-test ───────────────────────────────────────────────────────────
# A checker that has never been shown to go RED is a checker that prints PASS
# while checking nothing. Every rule below gets a fixture that MUST be refused
# and every accept-path gets a control that MUST pass.
if [ "$SELFTEST" -eq 1 ]; then
  fail=0
  # A refusal fixture asserts WHICH rule answered, not merely that "an error
  # happened". INSTALL-METHODOLOGY §10's M1 is the precedent: a fixture caught
  # by a neighbouring rule keeps a disabled guard looking green forever, and a
  # test that only checks "non-zero exit" cannot tell the two apart.
  t() { # t <expect ok|RULE-ID> <name> <json>
    local expect="$1" name="$2" json="$3" rc out
    printf '%s' "$json" > "/tmp/fdc-selftest.$$.json"
    out="$(validate_manifest "/tmp/fdc-selftest.$$.json" 2>&1)"; rc=$?
    rm -f "/tmp/fdc-selftest.$$.json"
    if [ "$expect" = ok ]; then
      if [ "$rc" -ne 0 ]; then echo "SELFTEST FAIL (control rejected): $name -- $out"; fail=1
      else echo "ok  control        $name"; fi
    elif [ "$rc" -eq 0 ]; then
      echo "SELFTEST FAIL (defect accepted): $name"; fail=1
    elif ! printf '%s' "$out" | grep -q "$expect"; then
      echo "SELFTEST FAIL (wrong rule answered): $name -- expected $expect, got: $(printf '%s' "$out" | head -1)"; fail=1
    else
      echo "ok  $expect  $name"
    fi
  }
  GOOD='{"schema":"vulos.debian-closure/1","app":"x","package":"x","suite":"trixie","base_image":"debian:trixie-slim","snapshot":"20260816T000000Z","arches":{"arm64":{"resolved_version":"1","package_count":1,"download_bytes":4,"packages":[{"name":"a","version":"1","filename":"a.deb","url":"https://snapshot.debian.org/archive/debian/20260816T000000Z/pool/main/a/a/a.deb","size":4,"sha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}}}'
  t ok  "CTRL-1 a well-formed one-package manifest is accepted" "$GOOD"
  t CLOSURE-01/09 "CLOSURE-01 a package with no sha256 is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"sha256":"0*"/"sha256":""/')"
  t CLOSURE-02/04 "CLOSURE-02 a deb.debian.org (moving) URL is refused" \
     "$(printf '%s' "$GOOD" | sed 's#https://snapshot.debian.org/archive/debian/20260816T000000Z#http://deb.debian.org/debian#')"
  t CLOSURE-03 "CLOSURE-03 a manifest with no base_image is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"base_image":"debian:trixie-slim"/"base_image":""/')"
  t CLOSURE-02/04 "CLOSURE-04 a http:// snapshot URL is refused" \
     "$(printf '%s' "$GOOD" | sed 's#https://snapshot#http://snapshot#')"
  t CLOSURE-05 "CLOSURE-05 two packages sharing one sha256 are refused" \
     '{"schema":"vulos.debian-closure/1","app":"x","package":"x","suite":"trixie","base_image":"b","snapshot":"20260816T000000Z","arches":{"arm64":{"resolved_version":"1","package_count":2,"download_bytes":8,"packages":[{"name":"a","version":"1","filename":"a.deb","url":"https://snapshot.debian.org/archive/debian/20260816T000000Z/a.deb","size":4,"sha256":"1111111111111111111111111111111111111111111111111111111111111111"},{"name":"b","version":"1","filename":"b.deb","url":"https://snapshot.debian.org/archive/debian/20260816T000000Z/b.deb","size":4,"sha256":"1111111111111111111111111111111111111111111111111111111111111111"}]}}}'
  t CLOSURE-06 "CLOSURE-06 package_count disagreeing with the list is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"package_count":1/"package_count":9/')"
  t CLOSURE-07 "CLOSURE-07 download_bytes disagreeing with the sizes is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"download_bytes":4/"download_bytes":99/')"
  t CLOSURE-08 "CLOSURE-08 a size of 0 is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"size":4/"size":0/')"
  t CLOSURE-01/09 "CLOSURE-09 a non-hex sha256 is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"sha256":"0\{64\}"/"sha256":"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"/')"
  t CLOSURE-10 "CLOSURE-10 an arch key that is not a Vulos arch spelling is refused" \
     "$(printf '%s' "$GOOD" | sed 's/"arm64":{/"aarch64":{/')"
  t CLOSURE-11 "CLOSURE-11 an empty arches map is refused" \
     '{"schema":"vulos.debian-closure/1","app":"x","package":"x","suite":"trixie","base_image":"b","snapshot":"20260816T000000Z","arches":{}}'
  t CLOSURE-12 "CLOSURE-12 a filename escaping the prefix is refused" \
     "$(printf '%s' "$GOOD" | sed 's#"filename":"a.deb"#"filename":"../a.deb"#')"
  t CLOSURE-13 "CLOSURE-13 an unknown schema is refused" \
     "$(printf '%s' "$GOOD" | sed 's#vulos.debian-closure/1#vulos.something-else/1#')"
  [ "$fail" -eq 0 ] && echo "SELFTEST: all fixtures behaved" || echo "SELFTEST: FAILURES ABOVE"
  exit "$fail"
fi

[ -n "$APP" ] && [ -n "$PKG" ] || { echo "freeze-debian-closure: --app and --package are required" >&2; exit 2; }
[ "${#ARCHES[@]}" -gt 0 ] || ARCHES=(arm64 amd64)
[ -n "$OUT" ] || OUT="registry.d/${APP}-debian-closure.json"
[ -n "$SNAPSHOT" ] || SNAPSHOT="$(date -u -v-1d +%Y%m%dT000000Z 2>/dev/null || date -u -d 'yesterday' +%Y%m%dT000000Z)"

command -v docker >/dev/null || { echo "freeze-debian-closure: docker is required" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
echo "freeze-debian-closure: app=$APP package=$PKG suite=$SUITE base=$BASE snapshot=$SNAPSHOT"
echo "freeze-debian-closure: arches=${ARCHES[*]}  resolve_only=$RESOLVE_ONLY"

for arch in "${ARCHES[@]}"; do
  case "$arch" in
    arm64) platform="linux/arm64" ;;
    amd64) platform="linux/amd64" ;;
    *) echo "freeze-debian-closure: unsupported arch $arch" >&2; exit 2 ;;
  esac
  echo "── resolving $PKG for $arch in $BASE ──"
  cat > "$WORK/in-$arch.sh" <<INNER
set -u
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null 2>&1 || { echo "APT_UPDATE_FAILED"; exit 3; }
[ "\$(dpkg --print-architecture)" = "$arch" ] || { echo "WRONG_ARCH \$(dpkg --print-architecture)"; exit 3; }
VER=\$(apt-cache policy "$PKG" 2>/dev/null | awk -F': ' '/Candidate:/{print \$2}')
[ -n "\$VER" ] && [ "\$VER" != "(none)" ] || { echo "NO_CANDIDATE"; exit 4; }
echo "RESOLVED_VERSION=\$VER"

# ── RESOLVE BEFORE INSTALLING ANYTHING. This order is load-bearing. ──────────
# \`--print-uris\` reports what is missing from the container it runs in. An
# earlier version of this script installed curl+python3 first, which dragged in
# libexpat1 and a dozen others, so those were absent from the closure while the
# manifest still declared base_image=debian:trixie-slim. The result installed
# cleanly on a pristine base and then died with
#   blender: error while loading shared libraries: libexpat.so.1
# That is CLOSURE-03's failure mode, and it was measured, not imagined
# (roadmap/DISTRO-SOURCED-APPS.md §4.4, attempt 3). The closure is resolved
# against the DECLARED base and nothing is installed until afterwards.
apt-get install --print-uris -y --no-install-recommends "$PKG" 2>/dev/null | grep "^'" > /uris.raw
echo "CLOSURE_COUNT=\$(wc -l < /uris.raw)"
echo "CLOSURE_BYTES=\$(awk '{s+=\$3} END{print s+0}' /uris.raw)"
if [ "$RESOLVE_ONLY" = "1" ]; then cat /uris.raw; exit 0; fi

# Safe to perturb the container now: the closure is already frozen above.
apt-get install -y -qq curl ca-certificates python3 >/dev/null 2>&1 || { echo "TOOLS_FAILED"; exit 3; }
python3 - "$SNAPSHOT" <<'PY' > /plan.json
import re,sys,json
snap=sys.argv[1]; out=[]
for line in open('/uris.raw'):
    m=re.match(r"^'([^']+)'\s+(\S+)\s+(\d+)",line.strip())
    if not m: continue
    live,fn,size=m.group(1),m.group(2),int(m.group(3))
    # deb.debian.org/debian/pool/... and /debian-security/pool/... map to two
    # different snapshot archives; getting this wrong yields a 404, never a
    # wrong file, because snapshot paths are content-addressed by pool layout.
    if '/debian-security/' in live:
        u=re.sub(r'^https?://[^/]+/debian-security/','https://snapshot.debian.org/archive/debian-security/%s/'%snap,live)
    else:
        u=re.sub(r'^https?://[^/]+/debian/','https://snapshot.debian.org/archive/debian/%s/'%snap,live)
    out.append({"filename":fn,"live_url":live,"url":u,"size":size})
json.dump(out,sys.stdout)
PY
mkdir -p /dl && cd /dl
python3 - <<'PY'
# Download from the URL THAT GOES IN THE MANIFEST, and hash those bytes.
# Never the live URL, never apt's MD5, never a value anybody typed.
import json,subprocess,hashlib,os,sys,time
from concurrent.futures import ThreadPoolExecutor
plan=json.load(open('/plan.json'))
res=[];bad=[]

# Fetch in PARALLEL. A sequential loop over a 350-package closure is not merely
# slow, it is unusable: the same closure took over 40 minutes one file at a time
# and about 5 minutes at -P 12 (roadmap/DISTRO-SOURCED-APPS.md §4.4). Kept
# modest because snapshot.debian.org is a single host and hammering it is both
# rude and counterproductive.
POOL=int(os.environ.get('FREEZE_PARALLEL','8'))

def fetch(o):
    p='/dl/'+os.path.basename(o['filename'])
    # --connect-timeout / --max-time are NOT optional here. A curl with neither
    # hung for five minutes on one deb.debian.org connection during the
    # measurement run that produced this tool, and one stalled socket in a
    # 350-file run looks exactly like a slow network. --speed-limit catches the
    # other shape: a connection that stays open and dribbles.
    rc=subprocess.call(['curl','-fsSL','--retry','4','--retry-delay','2',
                        '--connect-timeout','20','--max-time','300',
                        '--speed-limit','1024','--speed-time','30',
                        '-o',p,o['url']])
    if rc!=0 or not os.path.exists(p):
        if os.path.exists(p): os.unlink(p)   # never leave a partial behind
        return (o,'curl rc=%d'%rc)
    b=open(p,'rb').read()
    os.unlink(p)
    # SIZE BEFORE HASH. A truncated .deb hashes perfectly happily; one such file
    # (40960 bytes of an expected 417336) was produced during the measurement
    # run by a killed curl, and only the size check caught it. Hashing first
    # would have pinned a valid-looking digest for a broken payload.
    if len(b)!=o['size']:
        return (o,'size %d != apt index %d'%(len(b),o['size']))
    o['sha256']=hashlib.sha256(b).hexdigest()
    o['size']=len(b)
    nv=os.path.basename(o['filename'])[:-4].split('_')
    o['name']=nv[0]; o['version']=nv[1].replace('%3a',':') if len(nv)>1 else ''
    o.pop('live_url',None)
    return (o,None)

t0=time.time(); done=0
with ThreadPoolExecutor(max_workers=POOL) as ex:
    for o,err in ex.map(fetch,plan):
        done+=1
        if err: bad.append((o['filename'],o['url'],err))
        else:   res.append(o)
        if done%50==0:
            print("PROGRESS %d/%d  %.0fs"%(done,len(plan),time.time()-t0),file=sys.stderr)
print("FETCH_SECONDS=%.0f POOL=%d"%(time.time()-t0,POOL))
res.sort(key=lambda x:x['name'])
    if i%25==0: print("PROGRESS %d/%d"%(i,len(plan)),file=sys.stderr)
json.dump({"packages":res,"unfetchable":bad},open('/frozen.json','w'))
print("FETCHED=%d UNFETCHABLE=%d"%(len(res),len(bad)))
for f in bad[:10]: print("UNFETCHABLE",f)
PY
echo "---FROZEN-JSON-BEGIN---"
cat /frozen.json
echo
echo "---FROZEN-JSON-END---"
INNER
  docker run --rm --platform "$platform" -v "$WORK/in-$arch.sh:/in.sh:ro" "$BASE" bash /in.sh \
      > "$WORK/out-$arch.txt" 2>"$WORK/err-$arch.txt"
  rc=$?
  echo "  docker exit=$rc"
  grep -E '^(RESOLVED_VERSION|CLOSURE_COUNT|CLOSURE_BYTES|FETCHED|NO_CANDIDATE|WRONG_ARCH|APT_UPDATE_FAILED)' "$WORK/out-$arch.txt" || true
  if [ "$rc" -ne 0 ]; then
    echo "freeze-debian-closure: $arch FAILED (exit $rc) — nothing written. Last stderr:" >&2
    tail -5 "$WORK/err-$arch.txt" >&2
    exit "$rc"
  fi
done

[ "$RESOLVE_ONLY" -eq 1 ] && { echo "freeze-debian-closure: --resolve-only, no manifest written"; exit 0; }

python3 - "$WORK" "$APP" "$PKG" "$SUITE" "$BASE" "$SNAPSHOT" "$OUT" "${ARCHES[@]}" <<'PY'
import json,sys,re,os
work,app,pkg,suite,base,snap,out=sys.argv[1:8]
arches=sys.argv[8:]
man={"schema":"vulos.debian-closure/1","app":app,"package":pkg,"suite":suite,
     "base_image":base,"snapshot":snap,
     "_note":"Frozen by scripts/freeze-debian-closure.sh. Every sha256 was computed "
             "from the bytes the snapshot.debian.org URL beside it actually served. "
             "The box does NO dependency resolution: it fetches this fixed list and "
             "extracts each with `dpkg-deb -x` into the app's own prefix.",
     "arches":{}}
for a in arches:
    txt=open(os.path.join(work,"out-%s.txt"%a)).read()
    ver=re.search(r'^RESOLVED_VERSION=(.+)$',txt,re.M)
    blob=txt.split("---FROZEN-JSON-BEGIN---")[1].split("---FROZEN-JSON-END---")[0].strip()
    d=json.loads(blob)
    if d["unfetchable"]:
        print("REFUSING to write: %s has %d unfetchable packages"%(a,len(d["unfetchable"])))
        for f in d["unfetchable"][:10]: print("  ",f)
        sys.exit(1)
    pk=sorted(d["packages"],key=lambda x:x["name"])
    man["arches"][a]={"resolved_version":ver.group(1) if ver else "",
                      "package_count":len(pk),
                      "download_bytes":sum(x["size"] for x in pk),
                      "packages":pk}
json.dump(man,open(out,"w"),indent=1,sort_keys=False)
print("wrote",out)
for a,b in man["arches"].items():
    print("  %s: %s, %d packages, %d bytes (%.1f MB)"%(a,b["resolved_version"],b["package_count"],b["download_bytes"],b["download_bytes"]/1e6))
PY
rc=$?
[ "$rc" -eq 0 ] || { echo "freeze-debian-closure: manifest NOT written" >&2; exit "$rc"; }
echo "── validating what was just written ──"
validate_manifest "$OUT" || { echo "freeze-debian-closure: the manifest it produced is INVALID; removing it" >&2; rm -f "$OUT"; exit 1; }
