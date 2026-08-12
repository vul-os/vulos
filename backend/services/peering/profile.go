// profile.go — profile store, avatar resize (→ 256×256 WebP lossless), visibility.
//
// Storage layout (under dir, typically ~/.vulos/peering/profile/):
//
//	profile.json   — serialised ProfileData (display_name, bio, slug, visibility)
//	avatar.webp    — 256×256 WebP lossless image
//
// Visibility levels (per field):
//
//	public  — anyone may view the field
//	peers   — only approved contacts (profileContactChecker.IsApproved)
//	nobody  — hidden from all callers; stored locally only
//
// Default visibility: image=public, bio=peers, email=nobody.
//
// Routes (wired by RegisterProfileHandlers):
//
//	GET  /api/peering/profile              → own profile JSON
//	PUT  /api/peering/profile              → update fields (partial)
//	POST /api/peering/profile/image        → upload avatar → resize + WebP
//	GET  /api/peering/profile/image        → serve avatar (ETag, visibility-gated)
//
// All new identifiers carry the prefix profile/Profile to avoid clashing with
// symbols in contacts.go, identity.go, ws.go, etc. (zero redeclaration guarantee).
package peering

import (
	"bytes"
	"container/heap"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math/bits"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── Visibility constants ─────────────────────────────────────────────────────

// ProfileVisibility is the access level for a single profile field.
type ProfileVisibility string

const (
	// ProfileVisPublic means anyone may view the field.
	ProfileVisPublic ProfileVisibility = "public"

	// ProfileVisPeers means only approved contacts may view the field.
	ProfileVisPeers ProfileVisibility = "peers"

	// ProfileVisNobody hides the field from all callers.
	ProfileVisNobody ProfileVisibility = "nobody"
)

// profileValidVis reports whether v is a recognised visibility constant.
func profileValidVis(v ProfileVisibility) bool {
	return v == ProfileVisPublic || v == ProfileVisPeers || v == ProfileVisNobody
}

// ProfileFieldVisibility holds per-field visibility settings.
type ProfileFieldVisibility struct {
	Image ProfileVisibility `json:"image"`
	Bio   ProfileVisibility `json:"bio"`
	Email ProfileVisibility `json:"email"`
}

// profileDefaultVisibility returns the spec-mandated defaults.
func profileDefaultVisibility() ProfileFieldVisibility {
	return ProfileFieldVisibility{
		Image: ProfileVisPublic,
		Bio:   ProfileVisPeers,
		Email: ProfileVisNobody,
	}
}

// ─── Profile model ────────────────────────────────────────────────────────────

// ProfileData is the persisted profile record.
type ProfileData struct {
	VulosID       string                 `json:"vulos_id"`
	DisplayName   string                 `json:"display_name"`
	Bio           string                 `json:"bio"`
	VerifiedEmail bool                   `json:"verified_email"`
	Slug          string                 `json:"slug"`
	Visibility    ProfileFieldVisibility `json:"visibility"`
	UpdatedAt     time.Time              `json:"updated_at"`
	// ContentPubKey is the user's PUBLISHED X25519 content-encryption public key
	// (base64 std, 32 raw bytes), derived client-side from the master key
	// (src/lib/contentSeal.js deriveContentKeyPair). It is PUBLIC key material only
	// — the private half never leaves the client. Sharers wrap file content to this
	// key so a content-blind (cloud-relayed) share can only be opened by this user.
	// Empty means the user has not published one yet (older client / pre-wave-3):
	// content-blind shares to them must fail closed, never fall back to plaintext.
	ContentPubKey string `json:"content_pub_key,omitempty"`
}

// ─── Narrow interface ─────────────────────────────────────────────────────────

// profileContactChecker is the subset of *ContactStore that profile needs.
// *ContactStore satisfies this interface automatically.
type profileContactChecker interface {
	IsApproved(vulosID string) bool
}

// ─── Profile store ────────────────────────────────────────────────────────────

// ProfileStore persists ProfileData and the avatar on disk.
type ProfileStore struct {
	mu       sync.RWMutex
	dir      string
	data     ProfileData
	contacts profileContactChecker
}

const (
	profileJSONFile   = "profile.json"
	profileAvatarFile = "avatar.webp"
)

// NewProfileStore opens or initialises a ProfileStore at dir.
// contacts gates peer-visibility checks; may be nil (peer fields treated as nobody).
func NewProfileStore(dir, vulosID string, contacts profileContactChecker) (*ProfileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("profile: mkdir %s: %w", dir, err)
	}
	ps := &ProfileStore{dir: dir, contacts: contacts}
	if err := ps.profileLoad(vulosID); err != nil {
		return nil, err
	}
	return ps, nil
}

func (ps *ProfileStore) profileFilePath(name string) string {
	return filepath.Join(ps.dir, name)
}

// profileLoad reads profile.json; creates defaults if absent.
func (ps *ProfileStore) profileLoad(vulosID string) error {
	path := ps.profileFilePath(profileJSONFile)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		ps.data = ProfileData{
			VulosID:    vulosID,
			Visibility: profileDefaultVisibility(),
			UpdatedAt:  time.Now().UTC(),
		}
		return ps.profilePersist()
	}
	if err != nil {
		return fmt.Errorf("profile: open %s: %w", path, err)
	}
	defer f.Close()
	if decErr := json.NewDecoder(f).Decode(&ps.data); decErr != nil {
		return fmt.Errorf("profile: decode %s: %w", path, decErr)
	}
	return nil
}

// profilePersist writes profile.json atomically.
func (ps *ProfileStore) profilePersist() error {
	b, err := json.MarshalIndent(ps.data, "", "  ")
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}
	return profileAtomicWrite(ps.profileFilePath(profileJSONFile), b, 0600)
}

// profileAtomicWrite writes data to path via temp-file + rename.
// Prefixed "profileAtomic" to avoid clashing with identity.go's writeFile.
func profileAtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".profile-tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Get returns a snapshot of the profile data.
func (ps *ProfileStore) Get() ProfileData {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.data
}

// Update applies fn to the profile data under the write lock and persists.
func (ps *ProfileStore) Update(fn func(*ProfileData)) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	fn(&ps.data)
	ps.data.UpdatedAt = time.Now().UTC()
	return ps.profilePersist()
}

// AvatarPath returns the absolute path to avatar.webp.
func (ps *ProfileStore) AvatarPath() string {
	return ps.profileFilePath(profileAvatarFile)
}

// AvatarETag computes a quoted ETag from the first 8 bytes of the avatar SHA-256.
// Returns ("", false) when no avatar exists.
func (ps *ProfileStore) AvatarETag() (string, bool) {
	b, err := os.ReadFile(ps.AvatarPath())
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf(`"%x"`, sum[:8]), true
}

// profileCanViewImage reports whether callerID may see the avatar.
func (ps *ProfileStore) profileCanViewImage(callerID string) bool {
	ps.mu.RLock()
	vis := ps.data.Visibility.Image
	contacts := ps.contacts
	ps.mu.RUnlock()

	switch vis {
	case ProfileVisPublic:
		return true
	case ProfileVisPeers:
		return callerID != "" && contacts != nil && contacts.IsApproved(callerID)
	default:
		return false
	}
}

// ─── Avatar: resize + WebP encode ────────────────────────────────────────────

const profileAvatarDim = 256

// profileSaveAvatar decodes src, resizes to 256×256, encodes as VP8L WebP,
// and atomically writes the result to avatar.webp.
func (ps *ProfileStore) profileSaveAvatar(src io.Reader) error {
	img, _, err := image.Decode(src)
	if err != nil {
		return fmt.Errorf("profile: decode image: %w", err)
	}
	dst := image.NewNRGBA(image.Rect(0, 0, profileAvatarDim, profileAvatarDim))
	profileNNResize(dst, img)

	webp, err := profileEncodeVP8L(dst)
	if err != nil {
		return fmt.Errorf("profile: webp encode: %w", err)
	}
	return profileAtomicWrite(ps.AvatarPath(), webp, 0644)
}

// profileNNResize fills dst from src using nearest-neighbour sampling.
func profileNNResize(dst *image.NRGBA, src image.Image) {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	for dy := 0; dy < profileAvatarDim; dy++ {
		for dx := 0; dx < profileAvatarDim; dx++ {
			sx := sb.Min.X + dx*sw/profileAvatarDim
			sy := sb.Min.Y + dy*sh/profileAvatarDim
			dst.Set(dx, dy, src.At(sx, sy))
		}
	}
}

// ─── VP8L (lossless WebP) minimal encoder ────────────────────────────────────
//
// References:
//   https://developers.google.com/speed/webp/docs/riff_container
//   https://developers.google.com/speed/webp/docs/webp_lossless_bitstream_specification
//
// Strategy: literal-only (no LZ77 back-references). Each pixel's four channels
// are emitted as Huffman-coded literals. Huffman trees are built from actual
// per-channel symbol frequencies and encoded with a meta-Huffman tree as
// described in §5.2 of the spec.

// profileBW is a LSB-first bit writer.
type profileBW struct {
	buf   []byte
	cur   uint64
	nbits int
}

func (bw *profileBW) put(val uint64, n int) {
	bw.cur |= val << bw.nbits
	bw.nbits += n
	for bw.nbits >= 8 {
		bw.buf = append(bw.buf, byte(bw.cur))
		bw.cur >>= 8
		bw.nbits -= 8
	}
}

func (bw *profileBW) flush() []byte {
	if bw.nbits > 0 {
		bw.buf = append(bw.buf, byte(bw.cur))
	}
	return bw.buf
}

// profileHCode is a (code, length) pair.
type profileHCode struct {
	code uint32
	bits uint8
}

// profileHNode is a heap node for Huffman tree construction.
type profileHNode struct {
	w    int
	sym  int // ≥0 for leaf, -1 for internal
	l, r *profileHNode
}

type profileHHeap []*profileHNode

func (h profileHHeap) Len() int           { return len(h) }
func (h profileHHeap) Less(i, j int) bool { return h[i].w < h[j].w }
func (h profileHHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *profileHHeap) Push(x any)        { *h = append(*h, x.(*profileHNode)) }
func (h *profileHHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// profileBuildCodes builds canonical Huffman codes from a frequency table.
// nsym is the alphabet size; returned slice has length nsym.
// Symbols with freq==0 receive code length 0 (not used).
// Code lengths are clamped to maxBits (≤15 for VP8L).
func profileBuildCodes(freq []int, nsym, maxBits int) []profileHCode {
	out := make([]profileHCode, nsym)

	// Collect non-zero symbols.
	leaves := make([]*profileHNode, 0, nsym)
	for i := 0; i < nsym; i++ {
		if freq[i] > 0 {
			leaves = append(leaves, &profileHNode{w: freq[i], sym: i})
		}
	}
	switch len(leaves) {
	case 0:
		return out
	case 1:
		out[leaves[0].sym] = profileHCode{0, 1}
		return out
	}

	// Build min-heap and Huffman tree.
	h := profileHHeap(leaves)
	heap.Init(&h)
	for h.Len() > 1 {
		a, b := heap.Pop(&h).(*profileHNode), heap.Pop(&h).(*profileHNode)
		heap.Push(&h, &profileHNode{w: a.w + b.w, sym: -1, l: a, r: b})
	}
	root := heap.Pop(&h).(*profileHNode)

	// Assign code lengths via DFS.
	lens := make([]int, nsym)
	var dfs func(*profileHNode, int)
	dfs = func(n *profileHNode, d int) {
		if n.sym >= 0 {
			if d == 0 {
				d = 1
			}
			lens[n.sym] = d
			return
		}
		dfs(n.l, d+1)
		dfs(n.r, d+1)
	}
	dfs(root, 0)

	// Clamp lengths.
	for i, l := range lens {
		if l > maxBits {
			lens[i] = maxBits
		}
	}

	// Canonical codes: sort by (length, symbol), assign codes left-to-right.
	type sl struct{ sym, len int }
	var sls []sl
	for sym, l := range lens {
		if l > 0 {
			sls = append(sls, sl{sym, l})
		}
	}
	sort.Slice(sls, func(i, j int) bool {
		if sls[i].len != sls[j].len {
			return sls[i].len < sls[j].len
		}
		return sls[i].sym < sls[j].sym
	})
	code, prev := uint32(0), 0
	for _, s := range sls {
		if s.len > prev {
			code <<= uint(s.len - prev)
			prev = s.len
		}
		rev := bits.Reverse32(code) >> (32 - uint(s.len))
		out[s.sym] = profileHCode{rev, uint8(s.len)}
		code++
	}
	return out
}

// profileEncodeVP8L encodes a 256×256 NRGBA image as a VP8L lossless WebP.
func profileEncodeVP8L(img *image.NRGBA) ([]byte, error) {
	const dim = profileAvatarDim
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()

	// Collect pixels and build per-channel frequency tables.
	type pix struct{ a, r, g, bl uint8 }
	pixels := make([]pix, 0, W*H)
	fG := make([]int, 256)
	fR := make([]int, 256)
	fB := make([]int, 256)
	fA := make([]int, 256)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.NRGBAAt(x, y)
			pixels = append(pixels, pix{c.A, c.R, c.G, c.B})
			fG[c.G]++
			fR[c.R]++
			fB[c.B]++
			fA[c.A]++
		}
	}

	// VP8L requires trees to cover the full alphabet; pad with freq=1.
	for i := 0; i < 256; i++ {
		if fG[i] == 0 {
			fG[i] = 1
		}
		if fR[i] == 0 {
			fR[i] = 1
		}
		if fB[i] == 0 {
			fB[i] = 1
		}
		if fA[i] == 0 {
			fA[i] = 1
		}
	}

	cG := profileBuildCodes(fG, 256, 15)
	cR := profileBuildCodes(fR, 256, 15)
	cB := profileBuildCodes(fB, 256, 15)
	cA := profileBuildCodes(fA, 256, 15)
	// Distance tree: only symbol 0 used (no LZ77). One-symbol → length 1, code 0.
	cD := make([]profileHCode, 40)
	cD[0] = profileHCode{0, 1}

	bw := &profileBW{}

	// VP8L image header: (W-1) in 14 bits, (H-1) in 14 bits, alpha=1, version=0.
	bw.put(uint64(dim-1), 14)
	bw.put(uint64(dim-1), 14)
	bw.put(1, 1) // alpha used
	bw.put(0, 3) // version = 0

	// No transforms.
	bw.put(0, 1)

	// Write 5 Huffman trees: green, red, blue, alpha, distance.
	if err := profileWriteTree(bw, cG, 256); err != nil {
		return nil, err
	}
	if err := profileWriteTree(bw, cR, 256); err != nil {
		return nil, err
	}
	if err := profileWriteTree(bw, cB, 256); err != nil {
		return nil, err
	}
	if err := profileWriteTree(bw, cA, 256); err != nil {
		return nil, err
	}
	if err := profileWriteTree(bw, cD, 40); err != nil {
		return nil, err
	}

	// Emit literal pixels (green < 256 → literal; no back-references).
	for _, p := range pixels {
		g, r, bl, a := cG[p.g], cR[p.r], cB[p.bl], cA[p.a]
		bw.put(uint64(g.code), int(g.bits))
		bw.put(uint64(r.code), int(r.bits))
		bw.put(uint64(bl.code), int(bl.bits))
		bw.put(uint64(a.code), int(a.bits))
	}

	_ = W
	_ = H // used only implicitly through b.Min/Max iteration
	vp8lPayload := bw.flush()

	// RIFF/WEBP container.
	// VP8L chunk = signature byte (0x2F) + bitstream.
	chunk := append([]byte{0x2F}, vp8lPayload...)
	chunkSize := uint32(len(chunk))

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	riffSize := uint32(4 + 8 + chunkSize) // "WEBP" + "VP8L" + 4-byte size + data
	if chunkSize%2 != 0 {
		riffSize++
	}
	binary.Write(&buf, binary.LittleEndian, riffSize)
	buf.WriteString("WEBP")
	buf.WriteString("VP8L")
	binary.Write(&buf, binary.LittleEndian, chunkSize)
	buf.Write(chunk)
	if chunkSize%2 != 0 {
		buf.WriteByte(0)
	}

	return buf.Bytes(), nil
}

// kCLOrder is the VP8L code-length code order (§5.2.2).
var kCLOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

// profileWriteTree writes a Huffman tree descriptor into bw using VP8L §5.2 format.
func profileWriteTree(bw *profileBW, codes []profileHCode, nsym int) error {
	// Gather code lengths.
	lens := make([]uint32, nsym)
	for i, c := range codes {
		if i < nsym {
			lens[i] = uint32(c.bits)
		}
	}

	// Count distinct non-zero lengths.
	usedSyms := make([]int, 0, nsym)
	for i, l := range lens {
		if l > 0 {
			usedSyms = append(usedSyms, i)
		}
	}

	// Use simple code when ≤2 symbols.
	if len(usedSyms) <= 2 {
		bw.put(1, 1) // simple_code_or_complex = 1
		switch len(usedSyms) {
		case 0:
			bw.put(0, 1)
			bw.put(0, 1)
			bw.put(0, 1)
		case 1:
			bw.put(0, 1) // num_symbols = 0
			s := usedSyms[0]
			if s > 1 {
				bw.put(1, 1)
				bw.put(uint64(s), 8)
			} else {
				bw.put(0, 1)
				bw.put(uint64(s), 1)
			}
		case 2:
			bw.put(1, 1) // num_symbols = 1
			s0, s1 := usedSyms[0], usedSyms[1]
			bw.put(1, 1)
			bw.put(uint64(s0), 8)
			bw.put(uint64(s1), 8)
		}
		return nil
	}

	// Complex (normal) Huffman tree.
	bw.put(0, 1) // simple_code_or_complex = 0

	// use_length = 0 → encode all nsym lengths.
	bw.put(0, 1)

	// Build the code-length meta-Huffman tree.
	// The 19 code-length symbols are 0..15 (direct lengths) + 16,17,18 (run codes).
	// We only use symbols 0..15 (direct code lengths, no run encoding).
	// Frequency: count how often each length value 0..15 appears in lens[].
	clFreq := make([]int, 19)
	for _, l := range lens {
		if l < 19 {
			clFreq[l]++
		}
	}
	clCodes := profileBuildCodes(clFreq, 19, 7) // VP8L limits CL codes to 7 bits

	// Encode the code-length code lengths (CLCL) in kCLOrder, 3 bits each.
	// Find the last non-zero CLCL entry.
	clcl := make([]uint32, 19)
	for i := 0; i < 19; i++ {
		clcl[i] = uint32(clCodes[kCLOrder[i]].bits)
	}
	last := 18
	for last > 3 && clcl[last] == 0 {
		last--
	}
	numCLCL := last + 1
	bw.put(uint64(numCLCL-4), 4)
	for i := 0; i < numCLCL; i++ {
		bw.put(uint64(clcl[i]), 3)
	}

	// Encode the actual code lengths using the meta-Huffman codes.
	for _, l := range lens {
		c := clCodes[l]
		bw.put(uint64(c.code), int(c.bits))
	}
	return nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// profileSvc bundles state for HTTP handler methods.
type profileSvc struct {
	store   *ProfileStore
	vulosID string
}

// RegisterProfileHandlers wires the four profile routes onto mux.
//
//	dir     — storage directory, e.g. filepath.Join(home, ".vulos/peering/profile")
//	vulosID  — local Vula ID string (e.g. "vulos:ed25519:...")
//	contacts — used to resolve peer-visibility checks; may be nil
//
// It returns the *ProfileStore (nil on init failure) so callers can reuse it for
// adjacent seams — e.g. the internal content-key lookup the Vulos cell calls to
// enforce recipient-targeting on content-blind shares (RegisterContentKeyLookup).
func RegisterProfileHandlers(mux *http.ServeMux, dir, vulosID string, contacts profileContactChecker) *ProfileStore {
	store, err := NewProfileStore(dir, vulosID, contacts)
	if err != nil {
		log.Printf("[peering/profile] store init: %v", err)
		fail := func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"profile store unavailable"}`, http.StatusServiceUnavailable)
		}
		mux.HandleFunc("GET /api/peering/profile", fail)
		mux.HandleFunc("PUT /api/peering/profile", fail)
		mux.HandleFunc("POST /api/peering/profile/image", fail)
		mux.HandleFunc("GET /api/peering/profile/image", fail)
		return nil
	}
	svc := &profileSvc{store: store, vulosID: vulosID}
	mux.HandleFunc("GET /api/peering/profile", svc.handleGet)
	mux.HandleFunc("PUT /api/peering/profile", svc.handlePut)
	mux.HandleFunc("POST /api/peering/profile/image", svc.handlePostImage)
	mux.HandleFunc("GET /api/peering/profile/image", svc.handleGetImage)
	return store
}

// ContentPubKey returns the profile's published X25519 content public key (base64
// std), or "" if none has been published. Used by the internal content-key lookup.
func (ps *ProfileStore) ContentPubKey() string {
	return ps.Get().ContentPubKey
}

// ─── GET /api/peering/profile ─────────────────────────────────────────────────

type profileGetResp struct {
	VulosID       string                 `json:"vulos_id"`
	DisplayName   string                 `json:"display_name"`
	Bio           string                 `json:"bio"`
	VerifiedEmail bool                   `json:"verified_email"`
	Slug          string                 `json:"slug"`
	HasAvatar     bool                   `json:"has_avatar"`
	Visibility    ProfileFieldVisibility `json:"visibility"`
	UpdatedAt     time.Time              `json:"updated_at"`
	ContentPubKey string                 `json:"content_pub_key,omitempty"`
}

func (svc *profileSvc) handleGet(w http.ResponseWriter, r *http.Request) {
	d := svc.store.Get()
	_, hasAvatar := svc.store.AvatarETag()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(profileGetResp{
		VulosID:       d.VulosID,
		DisplayName:   d.DisplayName,
		Bio:           d.Bio,
		VerifiedEmail: d.VerifiedEmail,
		Slug:          d.Slug,
		HasAvatar:     hasAvatar,
		Visibility:    d.Visibility,
		UpdatedAt:     d.UpdatedAt,
		ContentPubKey: d.ContentPubKey,
	})
}

// ─── PUT /api/peering/profile ─────────────────────────────────────────────────

type profilePutReq struct {
	DisplayName   *string                 `json:"display_name"`
	Bio           *string                 `json:"bio"`
	Slug          *string                 `json:"slug"`
	Visibility    *ProfileFieldVisibility `json:"visibility"`
	ContentPubKey *string                 `json:"content_pub_key"`
}

func (svc *profileSvc) handlePut(w http.ResponseWriter, r *http.Request) {
	var req profilePutReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		profileWriteErr(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := svc.store.Update(func(d *ProfileData) {
		if req.DisplayName != nil {
			d.DisplayName = *req.DisplayName
		}
		if req.Bio != nil {
			d.Bio = *req.Bio
		}
		if req.Slug != nil {
			d.Slug = *req.Slug
		}
		if req.ContentPubKey != nil {
			// Accept only a valid 32-byte X25519 public key (base64 std) or "" to
			// clear it. Reject anything else so a malformed key never gets published
			// and silently break content-blind sharing.
			if v := strings.TrimSpace(*req.ContentPubKey); v == "" {
				d.ContentPubKey = ""
			} else if raw, err := base64.StdEncoding.DecodeString(v); err == nil && len(raw) == 32 {
				d.ContentPubKey = v
			}
		}
		if req.Visibility != nil {
			v := *req.Visibility
			if profileValidVis(v.Image) {
				d.Visibility.Image = v.Image
			}
			if profileValidVis(v.Bio) {
				d.Visibility.Bio = v.Bio
			}
			if profileValidVis(v.Email) {
				d.Visibility.Email = v.Email
			}
		}
	}); err != nil {
		log.Printf("[peering/profile] PUT persist: %v", err)
		profileWriteErr(w, "persist failed", http.StatusInternalServerError)
		return
	}
	svc.handleGet(w, r)
}

// ─── POST /api/peering/profile/image ──────────────────────────────────────────

func (svc *profileSvc) handlePostImage(w http.ResponseWriter, r *http.Request) {
	const maxBytes = 20 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	var src io.Reader
	ct := r.Header.Get("Content-Type")
	if len(ct) >= 9 && ct[:9] == "multipart" {
		if err := r.ParseMultipartForm(maxBytes); err != nil {
			profileWriteErr(w, "multipart parse failed", http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("image")
		if err != nil {
			profileWriteErr(w, "missing 'image' field", http.StatusBadRequest)
			return
		}
		defer f.Close()
		src = f
	} else {
		src = r.Body
	}

	if err := svc.store.profileSaveAvatar(src); err != nil {
		log.Printf("[peering/profile] POST image: %v", err)
		profileWriteErr(w, "image processing failed", http.StatusBadRequest)
		return
	}

	etag, _ := svc.store.AvatarETag()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"etag":   etag,
		"path":   "profile/" + profileAvatarFile,
	})
}

// ─── GET /api/peering/profile/image ───────────────────────────────────────────

func (svc *profileSvc) handleGetImage(w http.ResponseWriter, r *http.Request) {
	callerID := r.Header.Get("X-Vulos-ID")
	if !svc.store.profileCanViewImage(callerID) {
		profileWriteErr(w, "forbidden", http.StatusForbidden)
		return
	}

	etag, ok := svc.store.AvatarETag()
	if !ok {
		profileWriteErr(w, "no avatar", http.StatusNotFound)
		return
	}

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	data, err := os.ReadFile(svc.store.AvatarPath())
	if err != nil {
		profileWriteErr(w, "avatar read error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

// ─── Shared helper ────────────────────────────────────────────────────────────

func profileWriteErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
