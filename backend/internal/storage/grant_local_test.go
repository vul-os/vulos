package storage

// grant_local_test.go — D-STORE-LOCAL-DEFAULT.
//
// storagemode's default is now "local-fs": no object store, no credentials, no
// hosted service. That default is only honest if the LOCAL data plane actually
// moves bytes, so this file exercises it end to end against a real filesystem
// — write, read back, move, delete, and the traversal guard — using the same
// GrantBroker the Files service calls, with no endpoint configured (exactly
// the resolution a default box produces).
//
// It deliberately does NOT use a mock: the point is that the path a default
// install takes is executed for real. Nothing here touches the network; if any
// of it started requiring an object store, these tests fail rather than skip.

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localBroker returns a broker whose resolver has NO endpoint — i.e. the
// resolution a box in the default (local-fs) storage mode produces — rooted at
// a temp dir. It asserts the precondition rather than assuming it, so this
// suite can never silently degrade into testing the S3 path.
func localBroker(t *testing.T) (*GrantBroker, string) {
	t.Helper()
	root := t.TempDir()
	r := NewResolver(ResolverConfig{LocalRoot: root})
	res := r.Resolve(context.Background(), "u1")
	if res.Configured() {
		t.Fatalf("precondition: default resolution must be local-FS fallback, got endpoint %q", res.Endpoint)
	}
	return NewGrantBroker(r, STSConfig{}, 0), root
}

func TestLocalFS_PutGetRoundTrip(t *testing.T) {
	b, root := localBroker(t)
	ctx := context.Background()
	const key = "u1/drive/notes/hello.txt"
	payload := []byte("bytes that never leave this box\n")

	etag, err := b.PutContent(ctx, "u1", "vulos-u1", key, bytes.NewReader(payload), int64(len(payload)), "text/plain")
	if err != nil {
		t.Fatalf("PutContent on the default (local-fs) path: %v", err)
	}
	if etag != "" {
		t.Errorf("local-FS PutContent returns no ETag; got %q", etag)
	}

	// The bytes must be on this machine's disk, under LocalRoot, mirroring the key.
	onDisk := filepath.Join(root, filepath.FromSlash(key))
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("expected bytes at %s: %v", onDisk, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("on-disk content mismatch: got %q, want %q", got, payload)
	}

	rc, size, err := b.GetContent(ctx, "u1", "vulos-u1", key)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	defer rc.Close()
	if size != int64(len(payload)) {
		t.Errorf("GetContent size = %d, want %d", size, len(payload))
	}
	readBack, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(readBack, payload) {
		t.Fatalf("round-trip mismatch: got %q, want %q", readBack, payload)
	}
}

func TestLocalFS_GrantsAreLocalPathsNotPresignedURLs(t *testing.T) {
	b, root := localBroker(t)
	ctx := context.Background()
	const key = "u1/drive/report.odt"

	for _, tc := range []struct {
		name string
		mint func() (ObjectGrant, error)
		verb string
	}{
		{"read", func() (ObjectGrant, error) { return b.MintRead(ctx, "u1", "vulos-u1", key, 0) }, "GET"},
		{"write", func() (ObjectGrant, error) { return b.MintWrite(ctx, "u1", "vulos-u1", key, 0) }, "PUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := tc.mint()
			if err != nil {
				t.Fatalf("mint %s grant: %v", tc.name, err)
			}
			if g.Type != GrantLocal {
				t.Fatalf("default box minted a %q grant; want %q — a local box must not hand out cloud grants",
					g.Type, GrantLocal)
			}
			if g.URL != "" || g.Creds.AccessKey != "" || g.Creds.SecretKey != "" {
				t.Fatalf("local grant leaked remote material: url=%q creds=%+v", g.URL, g.Creds)
			}
			if g.Method != tc.verb {
				t.Errorf("Method = %q, want %q", g.Method, tc.verb)
			}
			want := filepath.Join(root, filepath.FromSlash(key))
			if g.LocalPath != want {
				t.Errorf("LocalPath = %q, want %q", g.LocalPath, want)
			}
			if g.ExpiresAt.IsZero() {
				t.Error("grant must carry an expiry even on the local path")
			}
		})
	}
}

func TestLocalFS_MoveAndDelete(t *testing.T) {
	b, root := localBroker(t)
	ctx := context.Background()
	const src = "u1/drive/a/old.txt"
	const dst = "u1/drive/b/new.txt"
	payload := []byte("move me")

	if _, err := b.PutContent(ctx, "u1", "vulos-u1", src, bytes.NewReader(payload), int64(len(payload)), ""); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	if err := b.MoveObject(ctx, "u1", "vulos-u1", src, dst); err != nil {
		t.Fatalf("MoveObject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(src))); !os.IsNotExist(err) {
		t.Fatalf("source still present after move (err=%v)", err)
	}
	got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dst)))
	if err != nil {
		t.Fatalf("destination missing after move: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("moved content mismatch: got %q, want %q", got, payload)
	}

	// A pending node (no bytes yet) must move without error.
	if err := b.MoveObject(ctx, "u1", "vulos-u1", "u1/drive/never-uploaded", "u1/drive/still-nothing"); err != nil {
		t.Fatalf("MoveObject on a pending node must be a no-op success: %v", err)
	}

	if err := b.DeleteObject(ctx, "u1", "vulos-u1", dst); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dst))); !os.IsNotExist(err) {
		t.Fatalf("object still on disk after delete (err=%v)", err)
	}
	// Deleting again is success — the caller only needs the bytes gone.
	if err := b.DeleteObject(ctx, "u1", "vulos-u1", dst); err != nil {
		t.Fatalf("second DeleteObject must succeed: %v", err)
	}
}

func TestLocalFS_KeyTraversalStaysUnderRoot(t *testing.T) {
	b, root := localBroker(t)
	ctx := context.Background()

	for _, key := range []string{
		"../../etc/passwd",
		"u1/../../../../etc/shadow",
		"/absolute/escape",
	} {
		g, err := b.MintWrite(ctx, "u1", "vulos-u1", key, 0)
		if err != nil {
			t.Fatalf("MintWrite(%q): %v", key, err)
		}
		if !strings.HasPrefix(g.LocalPath, root+string(os.PathSeparator)) && g.LocalPath != root {
			t.Fatalf("key %q escaped the local root: %q not under %q", key, g.LocalPath, root)
		}
	}
}

func TestLocalFS_GetMissingObjectErrors(t *testing.T) {
	b, _ := localBroker(t)
	if _, _, err := b.GetContent(context.Background(), "u1", "vulos-u1", "u1/drive/absent"); err == nil {
		t.Fatal("GetContent on a missing local object must fail closed, not return an empty reader")
	}
}
