package assistant

import (
	"context"
	"hash/fnv"
	"strings"
	"testing"

	"vulos/backend/services/ai"
)

// localFakeEmbedder is a deterministic, purely in-process embedder used in
// tests. It uses the hashing trick (bag-of-words → fixed-dim vector) so that
// texts sharing words land close in cosine space — enough to prove semantic
// retrieval returns topically-relevant messages, with ZERO network I/O. It
// certifies on-instance so NewMailIndex accepts it (mirrors the ONNX embedder).
type localFakeEmbedder struct{ calls int }

const fakeDim = 512

func (e *localFakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls++
	v := make([]float32, fakeDim)
	// Embed content words only (drop stopwords/short tokens) so the toy vector
	// tracks topic, the way real sentence embeddings downweight function words.
	for _, w := range distinctiveTerms(text) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%fakeDim] += 1
	}
	return v, nil
}

func (e *localFakeEmbedder) OnInstance() bool { return true }

// externalFakeEmbedder does NOT certify on-instance — it must be rejected.
type externalFakeEmbedder struct{}

func (e *externalFakeEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, fakeDim), nil
}

// --- Sovereignty: only on-instance embedders are accepted -------------------

func TestNewMailIndexRejectsNonLocalEmbedder(t *testing.T) {
	if _, err := NewMailIndex(t.TempDir(), &externalFakeEmbedder{}); err != ErrEmbedderNotLocal {
		t.Fatalf("expected ErrEmbedderNotLocal for uncertified embedder, got %v", err)
	}
	if _, err := NewMailIndex(t.TempDir(), nil); err != ErrEmbedderNotLocal {
		t.Fatalf("expected ErrEmbedderNotLocal for nil embedder, got %v", err)
	}
	if _, err := NewMailIndex(t.TempDir(), &localFakeEmbedder{}); err != nil {
		t.Fatalf("expected on-instance embedder to be accepted, got %v", err)
	}
}

// --- Indexing is incremental / idempotent -----------------------------------

func TestIndexIncrementalIdempotent(t *testing.T) {
	emb := &localFakeEmbedder{}
	idx, err := NewMailIndex(t.TempDir(), emb)
	if err != nil {
		t.Fatal(err)
	}
	fx := NewFixtureSource()
	auth := Auth{UserID: "alice"}

	if err := idx.Index(context.Background(), auth, fx); err != nil {
		t.Fatal(err)
	}
	if n := idx.Count(auth); n == 0 {
		t.Fatal("nothing indexed")
	}
	firstCalls := emb.calls
	if firstCalls == 0 {
		t.Fatal("embedder never called on first pass")
	}

	// Force a second pass (throttle would otherwise skip) and confirm no message
	// is re-embedded: idempotent by content hash.
	idx.lastRun["alice"] = idx.lastRun["alice"].Add(-indexThrottle * 2)
	if err := idx.Index(context.Background(), auth, fx); err != nil {
		t.Fatal(err)
	}
	if emb.calls != firstCalls {
		t.Fatalf("re-indexing unchanged mail re-embedded messages: %d → %d", firstCalls, emb.calls)
	}
}

// --- Semantic retrieval returns topically relevant messages -----------------

func TestSemanticRetrievalRelevant(t *testing.T) {
	emb := &localFakeEmbedder{}
	idx, err := NewMailIndex(t.TempDir(), emb)
	if err != nil {
		t.Fatal(err)
	}
	fx := NewFixtureSource()
	auth := Auth{UserID: "alice"}

	// A query that shares NO exact subject term with the invoice ("Invoice #4471
	// — $128.40 due Jul 5") but is topically about it.
	msgs, err := idx.Retrieve(context.Background(), auth, fx, "how much money do I owe for storage billing payment", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("no messages retrieved")
	}
	if !strings.Contains(msgs[0].Subject, "Invoice") {
		t.Errorf("top semantic hit not the invoice; got %q (all: %v)", msgs[0].Subject, subjects(msgs))
	}

	// A scheduling query should surface Priya's 1:1 reschedule.
	msgs, err = idx.Retrieve(context.Background(), auth, fx, "reschedule our weekly meeting to another day", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubject(msgs, "move our 1:1") {
		t.Errorf("scheduling query missed the 1:1 message; got %v", subjects(msgs))
	}
}

// --- Per-user isolation ------------------------------------------------------

func TestPerUserIsolation(t *testing.T) {
	emb := &localFakeEmbedder{}
	idx, err := NewMailIndex(t.TempDir(), emb)
	if err != nil {
		t.Fatal(err)
	}

	// Alice gets the fixture inbox; Bob gets an empty one.
	alice := Auth{UserID: "alice"}
	bob := Auth{UserID: "bob"}

	if err := idx.Index(context.Background(), alice, NewFixtureSource()); err != nil {
		t.Fatal(err)
	}
	if err := idx.Index(context.Background(), bob, &emptySource{}); err != nil {
		t.Fatal(err)
	}

	if idx.Count(alice) == 0 {
		t.Fatal("alice index empty")
	}
	if idx.Count(bob) != 0 {
		t.Fatalf("bob's index leaked alice's mail: count=%d", idx.Count(bob))
	}

	// Bob's semantic search must not surface Alice's invoice.
	msgs, err := idx.Retrieve(context.Background(), bob, &emptySource{}, "invoice storage payment due", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("bob retrieved %d messages from an empty mailbox — isolation breach: %v", len(msgs), subjects(msgs))
	}
}

// --- On-instance-only: the whole index path performs no network egress ------
//
// The index only accepts embedders that certify on-instance, and the fake used
// here makes ZERO network calls. This test documents/guards that the retrieval
// path is satisfied entirely by the local embedder + on-box vector store.
func TestRetrievalIsOnInstanceOnly(t *testing.T) {
	emb := &localFakeEmbedder{}
	idx, err := NewMailIndex(t.TempDir(), emb)
	if err != nil {
		t.Fatal(err)
	}
	auth := Auth{UserID: "alice"}
	if _, err := idx.Retrieve(context.Background(), auth, NewFixtureSource(), "contract renewal signature", 3); err != nil {
		t.Fatal(err)
	}
	if emb.calls == 0 {
		t.Fatal("expected the on-instance embedder to have done the work")
	}
}

// --- Assistant wired to the index uses semantic retrieval -------------------

func TestAssistantSemanticSearchGrounds(t *testing.T) {
	idx, err := NewMailIndex(t.TempDir(), &localFakeEmbedder{})
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeModel{reply: "You owe $128.40 to Tigris for storage."}
	a := New(m, localCfg(), NewFixtureSource(), false).WithIndex(idx)

	// Semantic query with no exact overlap with the invoice subject line.
	res, err := a.Search(context.Background(), Auth{UserID: "alice"}, "what do I owe for object storage")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubject(res.Results, "Invoice #4471") {
		t.Errorf("semantic search did not ground on the invoice; got %v", subjects(res.Results))
	}
	if !strings.Contains(m.lastReq.Messages[0].Content, "Invoice #4471") {
		t.Errorf("retrieved invoice not passed to the model context")
	}
}

// emptySource is a mailbox with no messages (for isolation tests).
type emptySource struct{}

func (emptySource) Name() string { return "empty" }
func (emptySource) Recent(context.Context, Auth, string, int) ([]Message, error) {
	return nil, nil
}
func (emptySource) Get(context.Context, Auth, string, string) (Message, error) {
	return Message{}, errNotFound
}
func (emptySource) Search(context.Context, Auth, string, string, int) ([]Message, error) {
	return nil, nil
}
func (emptySource) SaveDraft(context.Context, Auth, Draft) error { return nil }

func subjects(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Subject
	}
	return out
}

func containsSubject(msgs []Message, substr string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Subject, substr) {
			return true
		}
	}
	return false
}

// compile-time: fakeModel is defined in assistant_test.go; ensure ai import used.
var _ = ai.Message{}
