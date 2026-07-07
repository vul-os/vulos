package assistant

// index.go — the on-instance semantic mail index (RAG retrieval).
//
// This is the "real embedding index" the v1 lexical retriever documented as a
// drop-in point. It embeds the user's mail with an ON-INSTANCE embedder (the
// local ONNX model in services/embeddings — no external embedding API) and
// stores the vectors in the on-box vector store (internal/vecdb). Retrieval
// embeds the query and does a cosine-similarity ANN search, so the assistant
// finds messages by MEANING, not just keyword overlap. A lexical pass over the
// same on-box index is merged in so exact-term queries ("invoice #4471") still
// hit precisely — a hybrid retriever.
//
// SOVEREIGNTY: everything here stays on the box. The embedder must certify
// itself on-instance (Embedder.OnInstance) or NewMailIndex refuses it, so mail
// content can never be shipped to a third-party embedding API. The index files
// live under the instance's data dir, one per user (isolation).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/vecdb"
)

// Embedder turns text into a vector. It is deliberately the minimal seam the
// index needs; services/embeddings.OnnxEmbedder satisfies it.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ErrEmbedderNotLocal is returned when an embedder cannot certify that it runs
// on-instance. The mail index fails closed rather than risk shipping mail
// content to an off-box embedding service.
var ErrEmbedderNotLocal = errors.New("assistant: embedder is not certified on-instance — refusing to index mail (sovereignty)")

// Bounds keep the index cheap and safe on empty or very large mailboxes.
const (
	indexMaxDocs    = 2000             // hard cap on stored vectors per user (eviction beyond this)
	indexPullBatch  = 400              // messages pulled from the mail source per pass
	indexThrottle   = 20 * time.Second // minimum gap between index passes for a user
	indexEmbedChars = 4000             // cap on chars embedded per message
	defaultScope    = "default"
)

// MailIndex is a per-user, on-instance semantic index over mail. It is safe for
// concurrent use.
type MailIndex struct {
	dir string
	emb Embedder

	mu      sync.Mutex
	dbs     map[string]*vecdb.DB
	lastRun map[string]time.Time
}

// NewMailIndex builds an index rooted at dir, using emb for embeddings. It
// returns ErrEmbedderNotLocal unless emb certifies on-instance operation via an
// OnInstance() bool method returning true — this is the sovereign choke point
// for the embedding path (mirrors sovereign.go's Guard for the model path).
func NewMailIndex(dir string, emb Embedder) (*MailIndex, error) {
	if emb == nil {
		return nil, ErrEmbedderNotLocal
	}
	if lr, ok := emb.(interface{ OnInstance() bool }); !ok || !lr.OnInstance() {
		return nil, ErrEmbedderNotLocal
	}
	return &MailIndex{
		dir:     dir,
		emb:     emb,
		dbs:     make(map[string]*vecdb.DB),
		lastRun: make(map[string]time.Time),
	}, nil
}

// scope maps a user to an isolated index namespace. Empty → "default".
func scopeOf(auth Auth) string {
	s := strings.TrimSpace(auth.UserID)
	if s == "" {
		return defaultScope
	}
	return s
}

// getDB lazily opens (and caches) the per-user vector store. The filename is a
// hash of the scope so arbitrary user IDs are safe as paths and users are fully
// isolated on disk.
func (idx *MailIndex) getDB(scope string) (*vecdb.DB, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if db, ok := idx.dbs[scope]; ok {
		return db, nil
	}
	h := sha256.Sum256([]byte(scope))
	path := filepath.Join(idx.dir, "mail-"+hex.EncodeToString(h[:8])+".json")
	db, err := vecdb.Open(path)
	if err != nil {
		return nil, err
	}
	idx.dbs[scope] = db
	return db, nil
}

// Index incrementally embeds the user's recent mail into their vector store. It
// is idempotent: a message is (re)embedded only if it is new or its subject/body
// changed (tracked by a content hash), so repeated calls are cheap. Passes are
// throttled per user. Best-effort: per-message embed failures are skipped.
func (idx *MailIndex) Index(ctx context.Context, auth Auth, source MailSource) error {
	scope := scopeOf(auth)

	idx.mu.Lock()
	if last, ok := idx.lastRun[scope]; ok && time.Since(last) < indexThrottle {
		idx.mu.Unlock()
		return nil
	}
	idx.lastRun[scope] = time.Now()
	idx.mu.Unlock()

	db, err := idx.getDB(scope)
	if err != nil {
		return err
	}

	msgs, err := source.Recent(ctx, auth, defaultFolder, indexPullBatch)
	if err != nil {
		return err
	}

	changed := 0
	for _, m := range msgs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		text := embedText(m)
		if strings.TrimSpace(text) == "" {
			continue
		}
		hash := contentHash(text)
		if existing, ok := db.Get(m.UID); ok && existing.Metadata["hash"] == hash {
			continue // already indexed, unchanged
		}
		emb, err := idx.emb.Embed(ctx, text)
		if err != nil || len(emb) == 0 {
			continue // skip this message, keep going
		}
		raw, _ := json.Marshal(m)
		db.Upsert(&vecdb.Document{
			ID:        m.UID,
			Content:   text, // kept for the on-box lexical (exact-term) pass
			Embedding: emb,
			Metadata: map[string]string{
				"msg":  string(raw),
				"hash": hash,
				"date": m.Date.UTC().Format(time.RFC3339),
			},
		})
		changed++
	}

	idx.evict(db)
	if changed > 0 {
		_ = db.Flush()
	}
	return nil
}

// evict enforces the per-user doc cap by deleting the oldest messages first.
func (idx *MailIndex) evict(db *vecdb.DB) {
	if db.Count() <= indexMaxDocs {
		return
	}
	docs := db.All()
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].Metadata["date"] < docs[j].Metadata["date"] // oldest first
	})
	for i := 0; i < len(docs)-indexMaxDocs; i++ {
		db.Delete(docs[i].ID)
	}
}

// Retrieve returns the messages most relevant to query for this user, best-effort
// refreshing the index first. It is a hybrid: semantic (embedding) similarity
// plus an on-box exact-substring pass so precise term queries stay precise.
// Returns (nil, nil) when the index is empty or the query is blank, signalling
// the caller to fall back to its lexical path.
func (idx *MailIndex) Retrieve(ctx context.Context, auth Auth, source MailSource, query string, topK int) ([]Message, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	// Keep the index fresh; ignore errors so a transient mail/emb/ hiccup still
	// lets us search whatever is already indexed.
	_ = idx.Index(ctx, auth, source)

	db, err := idx.getDB(scopeOf(auth))
	if err != nil {
		return nil, err
	}
	if db.Count() == 0 {
		return nil, nil
	}

	qemb, err := idx.emb.Embed(ctx, q)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	out := make([]Message, 0, topK)

	// Semantic neighbours by cosine similarity are the primary signal: they rank
	// by MEANING, so a query need not share exact words with the message.
	for _, r := range db.Search(qemb, topK*3) {
		if len(out) >= topK {
			break
		}
		if seen[r.ID] {
			continue
		}
		if m, ok := messageFromDoc(&r.Document); ok {
			seen[r.ID] = true
			out = append(out, m)
		}
	}

	// Lexical supplement (hybrid): fold in any not-yet-included message that
	// contains a distinctive query term verbatim — this preserves precision for
	// exact-term queries (an invoice number, a message ID, a sender address) that
	// pure vector similarity might rank just outside top-k. Stopwords and very
	// short tokens are ignored so common words can't dominate.
	for _, term := range distinctiveTerms(q) {
		for _, d := range db.All() {
			if len(out) >= topK {
				break
			}
			if seen[d.ID] {
				continue
			}
			if strings.Contains(strings.ToLower(d.Content), term) {
				if m, ok := messageFromDoc(d); ok {
					seen[d.ID] = true
					out = append(out, m)
				}
			}
		}
	}

	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

// lexStopwords are common words excluded from the exact-term lexical pass so
// they can't spuriously match unrelated messages.
var lexStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "you": true, "your": true, "our": true,
	"are": true, "was": true, "with": true, "this": true, "that": true, "from": true,
	"have": true, "has": true, "had": true, "what": true, "when": true, "where": true,
	"who": true, "why": true, "how": true, "did": true, "does": true, "can": true,
	"about": true, "into": true, "out": true, "any": true, "all": true, "some": true,
	"they": true, "them": true, "his": true, "her": true, "she": true, "him": true,
	"were": true, "been": true, "will": true, "would": true, "could": true, "should": true,
}

// distinctiveTerms returns lowercased query tokens worth an exact-substring
// match: at least 4 chars and not a stopword.
func distinctiveTerms(q string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(q)) {
		if len(t) >= 4 && !lexStopwords[t] {
			out = append(out, t)
		}
	}
	return out
}

// Count reports how many messages are indexed for a user (status/telemetry).
func (idx *MailIndex) Count(auth Auth) int {
	db, err := idx.getDB(scopeOf(auth))
	if err != nil {
		return 0
	}
	return db.Count()
}

// ---- helpers ---------------------------------------------------------------

// embedText is the text embedded (and searched lexically) for a message:
// subject + body, body falling back to preview, bounded in size.
func embedText(m Message) string {
	body := m.Body
	if strings.TrimSpace(body) == "" {
		body = m.Preview
	}
	text := strings.TrimSpace(m.Subject + "\n\n" + body)
	if len(text) > indexEmbedChars {
		// Rune-safe clip: a byte-boundary cut can split a multi-byte UTF-8 rune,
		// and the on-instance ONNX embedder reads this text as UTF-8 on stdin —
		// invalid bytes there raise UnicodeDecodeError, silently dropping the
		// message from the semantic index. Clip on a rune boundary instead.
		text = clipUTF8(text, indexEmbedChars)
	}
	return text
}

func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

func messageFromDoc(d *vecdb.Document) (Message, bool) {
	raw := d.Metadata["msg"]
	if raw == "" {
		return Message{}, false
	}
	var m Message
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Message{}, false
	}
	return m, true
}
