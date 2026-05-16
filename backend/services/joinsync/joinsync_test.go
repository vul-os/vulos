package joinsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/cluster"
)

// mockBackend is an in-memory clusterBackend — NO real AWS / minio dialing.
type mockBackend struct {
	mu sync.Mutex

	// validateErr, when non-nil, is returned by validate.
	validateErr error
	// gotPassphrase records the passphrase passed to validate so a test can
	// assert it was used in memory and never leaked to disk.
	gotPassphrase string
	gotCfg        cluster.S3Config

	// pullErr, when non-nil, is returned by pull (after emitting progress).
	pullErr error
	// pullDone is closed when pull returns.
	pullDone chan struct{}
}

func newMockBackend() *mockBackend {
	return &mockBackend{pullDone: make(chan struct{})}
}

func (m *mockBackend) validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error {
	m.mu.Lock()
	m.gotPassphrase = passphrase
	m.gotCfg = cfg
	m.mu.Unlock()
	return m.validateErr
}

func (m *mockBackend) pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(string, int)) error {
	progress("connecting", 10)
	progress("downloading", 50)
	err := m.pullErr
	if err == nil {
		progress("finalizing", 95)
	}
	close(m.pullDone)
	return err
}

// withMock installs m as the active backend and restores the previous one.
func withMock(t *testing.T, m clusterBackend) {
	t.Helper()
	prev := backend
	backend = m
	t.Cleanup(func() { backend = prev })
}

func tmpHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// drainPull blocks until the mock's background pull goroutine has returned and
// written its terminal sync-state, so it cannot race with t.TempDir() cleanup
// (which removes the dir the goroutine writes into). Register AFTER tmpHome so
// it runs BEFORE TempDir removal (t.Cleanup is LIFO).
func drainPull(t *testing.T, m *mockBackend, home string) {
	t.Helper()
	t.Cleanup(func() {
		select {
		case <-m.pullDone:
		case <-time.After(5 * time.Second):
			t.Error("background pull did not finish before cleanup")
			return
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			st := Progress(home)
			if st.Status == "complete" || st.Status == "error" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

func validReq() JoinRequest {
	return JoinRequest{
		Bucket:     "vulos-cluster",
		Region:     "us-east-1",
		Access:     "AKIDEXAMPLE",
		Secret:     "secretkeyexample",
		Passphrase: "correct horse battery staple",
		Endpoint:   "localhost:9000",
	}
}

// --- validation / error mapping ---------------------------------------------

func TestJoin_RejectsMissingFields(t *testing.T) {
	withMock(t, newMockBackend())
	home := tmpHome(t)

	cases := map[string]JoinRequest{
		"no bucket":     {Access: "a", Secret: "s", Passphrase: "p"},
		"no access":     {Bucket: "b", Secret: "s", Passphrase: "p"},
		"no secret":     {Bucket: "b", Access: "a", Passphrase: "p"},
		"no passphrase": {Bucket: "b", Access: "a", Secret: "s"},
		"blank fields":  {Bucket: "  ", Access: " ", Secret: " ", Passphrase: " "},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Join(req, home)
			if !errors.Is(err, ErrBadRequest) {
				t.Fatalf("expected ErrBadRequest, got %v", err)
			}
		})
	}
}

func TestJoin_RejectsBadPassphrase(t *testing.T) {
	m := newMockBackend()
	m.validateErr = ErrBadPassphrase
	withMock(t, m)
	home := tmpHome(t)

	_, err := Join(validReq(), home)
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("expected ErrBadPassphrase, got %v", err)
	}

	// On a failed validation NOTHING must be persisted.
	if _, statErr := os.Stat(filepath.Join(home, ".vulos", "db", storageFileName)); statErr == nil {
		t.Fatal("storage.json was written despite a bad passphrase")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".vulos", "db", syncStateFileName)); statErr == nil {
		t.Fatal("sync-state.json was written despite a bad passphrase")
	}
}

func TestJoin_RejectsUnreachableBucket(t *testing.T) {
	m := newMockBackend()
	m.validateErr = errors.Join(ErrUnreachable, errors.New("dial tcp: connection refused"))
	withMock(t, m)
	home := tmpHome(t)

	_, err := Join(validReq(), home)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
}

// --- success path: file shapes + passphrase secrecy --------------------------

func TestJoin_PersistsStorageWithoutPassphrase(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	req := validReq()
	res, err := Join(req, home)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Status != "syncing" {
		t.Fatalf("expected status syncing, got %q", res.Status)
	}

	dbPath := filepath.Join(home, ".vulos", "db")

	storageRaw, err := os.ReadFile(filepath.Join(dbPath, storageFileName))
	if err != nil {
		t.Fatalf("read storage.json: %v", err)
	}

	// The passphrase MUST NOT appear anywhere in storage.json.
	if containsBytes(storageRaw, []byte(req.Passphrase)) {
		t.Fatal("passphrase leaked into storage.json")
	}

	// Assert the storageprov-compatible fields are present and correct.
	var sp struct {
		Enabled       bool   `json:"enabled"`
		AccessKey     string `json:"access_key"`
		SecretKey     string `json:"secret_key"`
		SSECKey       string `json:"ssec_key"`
		BucketName    string `json:"bucket_name"`
		ProvisionedAt string `json:"provisioned_at"`
		// CLUSTER-06 reader fields
		Endpoint string `json:"endpoint"`
		Bucket   string `json:"bucket"`
		Region   string `json:"region"`
	}
	if err := json.Unmarshal(storageRaw, &sp); err != nil {
		t.Fatalf("storage.json not valid JSON: %v", err)
	}
	if !sp.Enabled {
		t.Error("storage.json enabled should be true")
	}
	if sp.AccessKey != req.Access || sp.SecretKey != req.Secret {
		t.Error("storage.json access/secret not persisted correctly")
	}
	if sp.BucketName != req.Bucket || sp.Bucket != req.Bucket {
		t.Errorf("bucket fields wrong: bucket_name=%q bucket=%q", sp.BucketName, sp.Bucket)
	}
	if sp.Region != req.Region {
		t.Errorf("region not persisted: %q", sp.Region)
	}
	if sp.Endpoint != req.Endpoint {
		t.Errorf("endpoint not persisted: %q", sp.Endpoint)
	}
	if sp.ProvisionedAt == "" {
		t.Error("provisioned_at missing")
	}

	// No key may be named "passphrase" nor hold the passphrase value.
	var generic map[string]any
	if err := json.Unmarshal(storageRaw, &generic); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	for k, v := range generic {
		if s, ok := v.(string); ok && s == req.Passphrase {
			t.Fatalf("passphrase value found under key %q", k)
		}
		if k == "passphrase" {
			t.Fatal("a 'passphrase' key exists in storage.json")
		}
	}
}

func TestJoin_WritesSyncStateSyncing(t *testing.T) {
	m := newMockBackend()
	m.pullErr = errors.New("hold pull so we can observe syncing state")
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	if _, err := Join(validReq(), home); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// sync-state.json must exist with a status bootmode reads. It will be
	// "syncing" initially and may transition to "error" once the mock pull
	// fails — both are valid shapes; assert it was created correctly.
	var ss struct {
		Status      string `json:"status"`
		Phase       string `json:"phase"`
		ProgressPct int    `json:"progress_pct"`
		UpdatedAt   string `json:"updated_at"`
	}
	readJSONFile(t, filepath.Join(home, ".vulos", "db", syncStateFileName), &ss)
	if ss.Status == "" {
		t.Fatal("sync-state.json has empty status")
	}
	if ss.UpdatedAt == "" {
		t.Error("sync-state.json missing updated_at")
	}

	// instance.json must also exist so bootmode.Detect can reach the
	// sync-state rule (it returns "setup" if instance.json is absent).
	if _, err := os.Stat(filepath.Join(home, ".vulos", "db", "instance.json")); err != nil {
		t.Fatalf("instance.json not created: %v", err)
	}
}

func TestJoin_BackgroundPullCompletes(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	if _, err := Join(validReq(), home); err != nil {
		t.Fatalf("Join: %v", err)
	}

	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("background pull did not complete in time")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		st := Progress(home)
		if st.Status == "complete" {
			if st.ProgressPct != 100 {
				t.Errorf("expected progress 100 on complete, got %d", st.ProgressPct)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never reached complete; last=%+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	walkAssertNoPassphrase(t, filepath.Join(home, ".vulos"), validReq().Passphrase)
}

func TestJoin_BackgroundPullErrorRecorded(t *testing.T) {
	m := newMockBackend()
	m.pullErr = errors.New("simulated pull failure")
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	if _, err := Join(validReq(), home); err != nil {
		t.Fatalf("Join: %v", err)
	}

	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("background pull did not complete")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		st := Progress(home)
		if st.Status == "error" {
			if st.Message == "" {
				t.Error("error status should carry a message")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never reached error; last=%+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProgress_IdleWhenNoState(t *testing.T) {
	home := tmpHome(t)
	st := Progress(home)
	if st.Status != "idle" {
		t.Fatalf("expected idle, got %q", st.Status)
	}
}

func TestJoin_PassphrasePassedToBackendInMemoryOnly(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	req := validReq()
	if _, err := Join(req, home); err != nil {
		t.Fatalf("Join: %v", err)
	}

	m.mu.Lock()
	got := m.gotPassphrase
	gotBucket := m.gotCfg.Bucket
	m.mu.Unlock()

	if got != req.Passphrase {
		t.Fatalf("backend received passphrase %q, want %q", got, req.Passphrase)
	}
	if gotBucket != req.Bucket {
		t.Fatalf("backend received bucket %q, want %q", gotBucket, req.Bucket)
	}

	// Wait for the async pull, then assert the passphrase is nowhere on disk.
	select {
	case <-m.pullDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not finish")
	}
	walkAssertNoPassphrase(t, filepath.Join(home, ".vulos"), req.Passphrase)
}

// --- helpers -----------------------------------------------------------------

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

func walkAssertNoPassphrase(t *testing.T, root, passphrase string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// A TOCTOU race between background atomicWriteJSON's rename of a
			// .tmp file and filepath.Walk's internal lstat is benign: skip
			// entries that vanished between Walk's readdir and our visit.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			// File removed by the atomic rename between our visit and ReadFile;
			// that's fine — a .tmp scratch file that disappears is not a leak.
			if os.IsNotExist(rerr) {
				return nil
			}
			return rerr
		}
		if containsBytes(data, []byte(passphrase)) {
			t.Fatalf("passphrase found on disk in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}
