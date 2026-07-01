package vecdb

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Document represents an indexed item in the vector store.
type Document struct {
	ID        string            `json:"id"`
	Content   string            `json:"content"`
	Embedding []float32         `json:"embedding"`
	Metadata  map[string]string `json:"metadata"`
}

// SearchResult is a document with its similarity score.
type SearchResult struct {
	Document
	Score float64 `json:"score"`
}

// DB is a lightweight in-process vector database.
// Stores embeddings in memory with persistence to disk.
type DB struct {
	mu   sync.RWMutex
	docs map[string]*Document
	path string
}

// Open loads or creates a vector database at the given path.
func Open(path string) (*DB, error) {
	os.MkdirAll(filepath.Dir(path), 0755)
	db := &DB{
		docs: make(map[string]*Document),
		path: path,
	}
	if data, err := os.ReadFile(path); err == nil {
		var docs []*Document
		if err := json.Unmarshal(data, &docs); err == nil {
			for _, d := range docs {
				db.docs[d.ID] = d
			}
		}
	}
	return db, nil
}

// Upsert adds or updates a document.
func (db *DB) Upsert(doc *Document) {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.docs[doc.ID] = doc
}

// Delete removes a document by ID.
func (db *DB) Delete(id string) {
	db.mu.Lock()
	defer db.mu.Unlock()
	delete(db.docs, id)
}

// Get retrieves a document by ID.
func (db *DB) Get(id string) (*Document, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	d, ok := db.docs[id]
	return d, ok
}

// Search finds the top-k most similar documents to the query embedding.
func (db *DB) Search(queryEmb []float32, topK int) []SearchResult {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var results []SearchResult
	for _, doc := range db.docs {
		score := cosineSimilarity(queryEmb, doc.Embedding)
		results = append(results, SearchResult{Document: *doc, Score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}
	return results
}

// All returns a snapshot copy of every stored document. Order is unspecified.
// Used by callers that need to enforce size caps / eviction policies.
func (db *DB) All() []*Document {
	db.mu.RLock()
	defer db.mu.RUnlock()
	out := make([]*Document, 0, len(db.docs))
	for _, d := range db.docs {
		cp := *d
		out = append(out, &cp)
	}
	return out
}

// Count returns the number of documents.
func (db *DB) Count() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.docs)
}

// Flush persists the database to disk.
func (db *DB) Flush() error {
	db.mu.RLock()
	defer db.mu.RUnlock()

	docs := make([]*Document, 0, len(db.docs))
	for _, d := range db.docs {
		docs = append(docs, d)
	}

	data, err := json.Marshal(docs)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	tmp := db.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return os.Rename(tmp, db.path)
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
