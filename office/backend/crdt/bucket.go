package crdt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Snapshot is the cold-path bootstrap blob: state for the CRDT kind
// plus the retained op-log (so a late joiner can apply any ops that
// arrived after the snapshot but before connection).
type Snapshot struct {
	Kind    DocKind      `json:"kind"`
	Session string       `json:"session"`
	Clock   VectorClock  `json:"clock"`
	Log     []Op         `json:"log"`
	Text    textSnapshot `json:"text,omitempty"`
	Grid    gridSnapshot `json:"grid,omitempty"`
	Tree    treeSnapshot `json:"tree,omitempty"`
}

// Bucket is the cold-path persistence boundary. Implementations write
// snapshots + op-log entries somewhere durable (local FS, object
// store, Vulos cloud bucket). The Doc layer is bucket-agnostic.
type Bucket interface {
	SaveSnapshot(ctx context.Context, session string, snap Snapshot) error
	LoadSnapshot(ctx context.Context, session string) (Snapshot, bool, error)
	AppendOp(ctx context.Context, session string, op Op) error
	LoadOps(ctx context.Context, session string) ([]Op, error)
}

// MemBucket is an in-memory bucket for tests and ephemeral sessions.
type MemBucket struct {
	mu        sync.Mutex
	snapshots map[string]Snapshot
	ops       map[string][]Op
}

// NewMemBucket constructs an empty in-memory bucket.
func NewMemBucket() *MemBucket {
	return &MemBucket{snapshots: map[string]Snapshot{}, ops: map[string][]Op{}}
}

func (b *MemBucket) SaveSnapshot(_ context.Context, session string, snap Snapshot) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshots[session] = snap
	return nil
}

func (b *MemBucket) LoadSnapshot(_ context.Context, session string) (Snapshot, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.snapshots[session]
	return s, ok, nil
}

func (b *MemBucket) AppendOp(_ context.Context, session string, op Op) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ops[session] = append(b.ops[session], op)
	return nil
}

func (b *MemBucket) LoadOps(_ context.Context, session string) ([]Op, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.ops[session]
	out := make([]Op, len(src))
	copy(out, src)
	return out, nil
}

// FileBucket persists snapshots + op-logs as JSON files under a
// directory. Suitable as the local cold-store and as a stand-in for
// a real Vulos cloud bucket while we wire OFFICE-20.
//
// Layout:
//   root/
//     <session>.snapshot.json
//     <session>.ops.jsonl
type FileBucket struct {
	mu   sync.Mutex
	root string
}

// NewFileBucket creates (or opens) a FileBucket rooted at dir.
func NewFileBucket(dir string) (*FileBucket, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileBucket{root: dir}, nil
}

func (b *FileBucket) snapPath(session string) string {
	return filepath.Join(b.root, session+".snapshot.json")
}

func (b *FileBucket) opsPath(session string) string {
	return filepath.Join(b.root, session+".ops.jsonl")
}

func (b *FileBucket) SaveSnapshot(_ context.Context, session string, snap Snapshot) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	bs, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	tmp := b.snapPath(session) + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.snapPath(session))
}

func (b *FileBucket) LoadSnapshot(_ context.Context, session string) (Snapshot, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bs, err := os.ReadFile(b.snapPath(session))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, false, nil
		}
		return Snapshot{}, false, err
	}
	var s Snapshot
	if err := json.Unmarshal(bs, &s); err != nil {
		return Snapshot{}, false, err
	}
	return s, true, nil
}

func (b *FileBucket) AppendOp(_ context.Context, session string, op Op) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, err := os.OpenFile(b.opsPath(session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	bs, err := json.Marshal(op)
	if err != nil {
		return err
	}
	bs = append(bs, '\n')
	_, err = f.Write(bs)
	return err
}

func (b *FileBucket) LoadOps(_ context.Context, session string) ([]Op, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bs, err := os.ReadFile(b.opsPath(session))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Op
	start := 0
	for i, c := range bs {
		if c == '\n' {
			if i > start {
				var op Op
				if err := json.Unmarshal(bs[start:i], &op); err == nil {
					out = append(out, op)
				}
			}
			start = i + 1
		}
	}
	return out, nil
}
