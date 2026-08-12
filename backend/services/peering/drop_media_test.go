package peering

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestDropMediaStore(t *testing.T) (*MediaStore, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	home := t.TempDir()
	ms, err := NewMediaStore(home, priv, nil)
	if err != nil {
		t.Fatalf("NewMediaStore: %v", err)
	}
	return ms, home
}

// TestDropTransferSend verifies that SendFile ingests the local file into the
// content store and POSTs a well-formed pending-drop notice to the peer, with a
// signed download URL built against the node's own base URL.
func TestDropTransferSend(t *testing.T) {
	ms, home := newTestDropMediaStore(t)

	// A local file to share.
	src := filepath.Join(home, "hello.txt")
	content := []byte("hello peer, this is a drop")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	// Capture the peer's inbound notice.
	var got dropInboundRequest
	notified := make(chan struct{}, 1)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dropNotifyPath {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&got) //nolint:errcheck
		w.WriteHeader(http.StatusOK)
		notified <- struct{}{}
	}))
	defer peer.Close()

	dt := NewDropTransfer(ms, nil, "vulos:self", "Self", "https://self.example.org", filepath.Join(home, "Downloads"))
	txID, err := dt.SendFile(context.Background(), peer.URL, src, "text/plain")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if !strings.HasPrefix(txID, "drop-") {
		t.Fatalf("transfer id: want drop- prefix, got %q", txID)
	}

	<-notified

	if got.TransferID != txID {
		t.Fatalf("notice transfer id: want %q, got %q", txID, got.TransferID)
	}
	if got.FileName != "hello.txt" {
		t.Fatalf("notice file name: want hello.txt, got %q", got.FileName)
	}
	if got.FileSize != int64(len(content)) {
		t.Fatalf("notice file size: want %d, got %d", len(content), got.FileSize)
	}
	if got.FromVulosID != "vulos:self" {
		t.Fatalf("notice from: want vulos:self, got %q", got.FromVulosID)
	}
	if !strings.HasPrefix(got.DownloadURL, "https://self.example.org/api/peering/media/fetch/sha256:") {
		t.Fatalf("notice download url unexpected: %q", got.DownloadURL)
	}
	if !strings.Contains(got.DownloadURL, "sig=") || !strings.Contains(got.DownloadURL, "exp=") {
		t.Fatalf("download url not signed: %q", got.DownloadURL)
	}

	// The blob must now be in the content store.
	hexHash, err := hexHashFromFetchURL(got.DownloadURL)
	if err != nil {
		t.Fatalf("parse hash from url: %v", err)
	}
	if !ms.HasBlob(hexHash) {
		t.Fatalf("blob not stored after send")
	}
}

// TestDropTransferSendNoBaseURL verifies a clear error (not a silent bad URL)
// when the node has no reachable base URL configured.
func TestDropTransferSendNoBaseURL(t *testing.T) {
	ms, home := newTestDropMediaStore(t)
	src := filepath.Join(home, "x.txt")
	os.WriteFile(src, []byte("x"), 0o644) //nolint:errcheck
	dt := NewDropTransfer(ms, nil, "vulos:self", "Self", "", "")
	if _, err := dt.SendFile(context.Background(), "http://peer.invalid", src, "text/plain"); err == nil {
		t.Fatalf("expected error when selfBaseURL is empty")
	}
}

// TestDropTransferReceive verifies that ReceiveFile fetches the blob from the
// sender's signed URL, hash-verifies it, and lands a user-visible copy under
// the recipient's Downloads directory.
func TestDropTransferReceive(t *testing.T) {
	// Sender side: store a blob and serve it via the media handlers.
	senderMS, _ := newTestDropMediaStore(t)
	content := []byte("a shared photo's bytes")
	hexHash, err := storeForTest(senderMS, content, "image/jpeg", "photo.jpg")
	if err != nil {
		t.Fatalf("store sender blob: %v", err)
	}

	mux := http.NewServeMux()
	senderMS.RegisterMediaHandlers(mux)
	sender := httptest.NewServer(mux)
	defer sender.Close()

	signedURL := senderMS.buildSignedURL(sender.URL, hexHash, mediaSignedURLTTL)

	// Receiver side.
	recvMS, recvHome := newTestDropMediaStore(t)
	downloadDir := filepath.Join(recvHome, "Downloads")
	dt := NewDropTransfer(recvMS, nil, "vulos:recv", "Recv", "https://recv.example.org", downloadDir)

	if err := dt.ReceiveFile(context.Background(), signedURL, "photo.jpg", "image/jpeg"); err != nil {
		t.Fatalf("ReceiveFile: %v", err)
	}

	landed := filepath.Join(downloadDir, "photo.jpg")
	got, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("read landed file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("landed content mismatch: want %q got %q", content, got)
	}
	if !recvMS.HasBlob(hexHash) {
		t.Fatalf("receiver content store missing blob")
	}
}

// storeForTest stores content in ms and returns its hex hash.
func storeForTest(ms *MediaStore, content []byte, mime, name string) (string, error) {
	sum := sha256.Sum256(content)
	h := hex.EncodeToString(sum[:])
	_, err := ms.storeBlob(h, strings.NewReader(string(content)), mime, name)
	return h, err
}
