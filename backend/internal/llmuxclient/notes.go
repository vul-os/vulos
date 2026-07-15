package llmuxclient

// notes.go — SQLite-backed note embedding store + embed cache.
//
// Tables (see migrations/0001_llmuxclient.sql):
//   embed_cache      — keyed by SHA256(model||input), 30-day TTL
//   note_embeddings  — per-note vectors for semantic search

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite"
)

// embedTTL is the default cache TTL for stored embeddings.
const embedTTL = 30 * 24 * time.Hour

// NoteEmbedding holds a single note's stored embedding.
type NoteEmbedding struct {
	NoteID    string
	ModelSlug string
	Vector    []float32
	UpdatedAt time.Time
}

// EmbedRequest is the HTTP request body for POST /api/ai/embed.
type EmbedRequest struct {
	Input string `json:"input"`
	Model string `json:"model,omitempty"`
}

// EmbedResponse is returned by POST /api/ai/embed.
type EmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Model     string    `json:"model"`
}

// embedStats tracks in-memory hit/miss counters (best-effort; not persisted).
type embedStats struct {
	hits   atomic.Int64
	misses atomic.Int64
}

// Store is the SQLite-backed embedding store for llmuxclient.
type Store struct {
	db *sql.DB
}

// NewStore opens (creating if needed) the SQLite database at path and runs
// migrations. Returns an error on any failure.
func NewStore(path string) (*Store, error) {
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// NewStoreFromEnv opens the store using the LLMUXCLIENT_DB env var if set,
// otherwise joins dbDir with "llmuxclient.db". Opens SQLite and runs migrations.
func NewStoreFromEnv(dbDir string) (*Store, error) {
	dbPath := os.Getenv("LLMUXCLIENT_DB")
	if dbPath == "" {
		dbPath = filepath.Join(dbDir, "llmuxclient.db")
	}
	return NewStore(dbPath)
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// UpsertNoteEmbedding stores or replaces the embedding for (note_id, model_slug).
func (s *Store) UpsertNoteEmbedding(ne NoteEmbedding) error {
	blob := float32ToBlob(ne.Vector)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT INTO note_embeddings(note_id, model_slug, vector_blob, dim, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(note_id, model_slug) DO UPDATE SET
		   vector_blob=excluded.vector_blob,
		   dim=excluded.dim,
		   updated_at=excluded.updated_at`,
		ne.NoteID, ne.ModelSlug, blob, len(ne.Vector), now,
	)
	return err
}

// DeleteNoteEmbedding removes all embeddings for note_id.
func (s *Store) DeleteNoteEmbedding(noteID string) error {
	_, err := s.db.Exec(`DELETE FROM note_embeddings WHERE note_id=?`, noteID)
	return err
}

// ListNoteEmbeddings returns all stored note embeddings.
func (s *Store) ListNoteEmbeddings() ([]NoteEmbedding, error) {
	rows, err := s.db.Query(
		`SELECT note_id, model_slug, vector_blob, dim, updated_at FROM note_embeddings`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NoteEmbedding
	for rows.Next() {
		var ne NoteEmbedding
		var blob []byte
		var dim int
		var updatedAt string
		if err := rows.Scan(&ne.NoteID, &ne.ModelSlug, &blob, &dim, &updatedAt); err != nil {
			return nil, err
		}
		ne.Vector, _ = blobToFloat32(blob)
		ne.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, ne)
	}
	return out, rows.Err()
}

// SemanticSearchNotes returns the top-k note IDs (and scores) whose embedding
// has cosine similarity >= threshold with queryVec.
func (s *Store) SemanticSearchNotes(queryVec []float32, topK int, threshold float64) ([]string, []float64, error) {
	embeddings, err := s.ListNoteEmbeddings()
	if err != nil {
		return nil, nil, err
	}

	type scored struct {
		noteID string
		score  float64
	}

	bestByNote := map[string]float64{}
	for _, ne := range embeddings {
		sim, simErr := CosineSimilarity(queryVec, ne.Vector)
		if simErr != nil {
			continue
		}
		score := float64(sim)
		if score < threshold {
			continue
		}
		if prev, ok := bestByNote[ne.NoteID]; !ok || score > prev {
			bestByNote[ne.NoteID] = score
		}
	}

	results := make([]scored, 0, len(bestByNote))
	for id, sc := range bestByNote {
		results = append(results, scored{noteID: id, score: sc})
	}
	// Insertion-sort (small N in practice).
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].score > results[j-1].score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	noteIDs := make([]string, len(results))
	scores := make([]float64, len(results))
	for i, r := range results {
		noteIDs[i] = r.noteID
		scores[i] = r.score
	}
	return noteIDs, scores, nil
}

// CacheGet fetches a non-expired embedding from SQLite by cache key.
func (s *Store) CacheGet(key string) ([]float32, error) {
	var blob []byte
	var expiresAt string
	err := s.db.QueryRow(
		`SELECT vector_blob, expires_at FROM embed_cache WHERE cache_key=?`, key,
	).Scan(&blob, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("cache miss")
	}
	if err != nil {
		return nil, err
	}
	exp, parseErr := time.Parse(time.RFC3339, expiresAt)
	if parseErr != nil || time.Now().UTC().After(exp) {
		_, _ = s.db.Exec(`DELETE FROM embed_cache WHERE cache_key=?`, key)
		return nil, fmt.Errorf("cache expired")
	}
	return blobToFloat32(blob)
}

// CacheSet stores an embedding in SQLite with a 30-day TTL.
func (s *Store) CacheSet(key, model, input string, vec []float32) error {
	blob := float32ToBlob(vec)
	inputHash := hex.EncodeToString(func() []byte {
		h := sha256.Sum256([]byte(input))
		return h[:]
	}())
	now := time.Now().UTC()
	exp := now.Add(embedTTL).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO embed_cache
		 (cache_key, model, input_hash, vector_blob, dim, created_at, expires_at)
		 VALUES(?,?,?,?,?,?,?)`,
		key, model, inputHash, blob, len(vec), nowStr, exp,
	)
	return err
}

// CacheCount returns the number of cached embeddings.
func (s *Store) CacheCount() int64 {
	var n int64
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM embed_cache`).Scan(&n)
	return n
}

// ---------------------------------------------------------------------------
// Vector similarity
// ---------------------------------------------------------------------------

// CosineSimilarity computes cosine similarity between two equal-length vectors.
// Returns a value in [-1, 1]; higher means more similar.
func CosineSimilarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vector length mismatch: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("zero-length vectors")
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0, nil
	}
	return float32(dot / denom), nil
}

// ---------------------------------------------------------------------------
// Binary encoding helpers (little-endian float32)
// ---------------------------------------------------------------------------

func float32ToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		binary.LittleEndian.PutUint32(buf[i*4:], bits)
	}
	return buf
}

func blobToFloat32(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("blob length %d not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cache key
// ---------------------------------------------------------------------------

// EmbedCacheKey returns the canonical cache key for a (model, input) pair.
func EmbedCacheKey(model, input string) string {
	h := sha256.Sum256([]byte(model + "\x00" + input))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// DB helpers
// ---------------------------------------------------------------------------

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func runMigrations(db *sql.DB) error {
	return dbmigrate.Apply(db, migrationsFS, "migrations")
}
