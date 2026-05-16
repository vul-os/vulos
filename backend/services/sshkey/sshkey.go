// Package sshkey provides SSH host key management and authorized_keys persistence
// for headless/recovery access to Vulos-managed machines.
package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// hostKeyPath returns the path to the Ed25519 host private key.
func hostKeyPath(home string) string {
	return filepath.Join(home, ".vulos", "db", "ssh_host_ed25519_key")
}

// EnsureHostKey generates an Ed25519 host key at
// <home>/.vulos/db/ssh_host_ed25519_key (mode 0600) if absent.
// It is idempotent: if the file already exists it is read and returned.
// Returns the OpenSSH-format public key and its SHA256 fingerprint.
func EnsureHostKey(home string) (pubKey []byte, fingerprint string, err error) {
	path := hostKeyPath(home)

	// Ensure parent directory exists.
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, "", fmt.Errorf("sshkey: mkdir %s: %w", filepath.Dir(path), err)
	}

	// Attempt to read an existing key.
	if data, readErr := os.ReadFile(path); readErr == nil {
		signer, parseErr := ssh.ParsePrivateKey(data)
		if parseErr == nil {
			pub := signer.PublicKey()
			fp := ssh.FingerprintSHA256(pub)
			return ssh.MarshalAuthorizedKey(pub), fp, nil
		}
		// Corrupt key — regenerate below.
	}

	// Generate a new Ed25519 key pair.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("sshkey: generate key: %w", err)
	}

	// Marshal private key to OpenSSH PEM format.
	privPEM, err := marshalED25519PrivateKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("sshkey: marshal private key: %w", err)
	}

	// Write with mode 0600 (owner read/write only).
	if err = os.WriteFile(path, privPEM, 0600); err != nil {
		return nil, "", fmt.Errorf("sshkey: write key: %w", err)
	}

	// Build SSH public key.
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, "", fmt.Errorf("sshkey: create signer: %w", err)
	}
	pub := signer.PublicKey()
	fp := ssh.FingerprintSHA256(pub)
	return ssh.MarshalAuthorizedKey(pub), fp, nil
}

// marshalED25519PrivateKey returns the OpenSSH-format PEM for an Ed25519 private key.
// golang.org/x/crypto/ssh provides MarshalPrivateKey.
func marshalED25519PrivateKey(priv ed25519.PrivateKey) ([]byte, error) {
	pemBlock, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(pemBlock), nil
}

// AuthorizedKey represents a single entry in an authorized_keys file.
type AuthorizedKey struct {
	Comment     string `json:"comment"`
	PubKey      string `json:"pubkey"`
	Fingerprint string `json:"fingerprint"`
}

// AuthStore persists authorized public keys to disk.
type AuthStore struct {
	mu   sync.Mutex
	path string
}

// authorizedKeysPath returns the path to the authorized_keys file for home.
func authorizedKeysPath(home string) string {
	return filepath.Join(home, ".ssh", "authorized_keys")
}

// Open returns an AuthStore targeting <home>/.ssh/authorized_keys.
func Open(home string) (*AuthStore, error) {
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return nil, fmt.Errorf("sshkey: mkdir %s: %w", sshDir, err)
	}
	path := authorizedKeysPath(home)
	// Touch the file if absent so it exists with the right mode.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("sshkey: create authorized_keys: %w", err)
	}
	f.Close()
	// Enforce 0600 regardless of umask.
	if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("sshkey: chmod authorized_keys: %w", err)
	}
	return &AuthStore{path: path}, nil
}

// List returns all authorized keys currently persisted.
func (s *AuthStore) List() ([]AuthorizedKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readAll()
}

// Add validates and appends a new public key. Returns an error if the key is
// already present (same fingerprint) or fails to parse.
func (s *AuthStore) Add(comment, pubkey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubkey))
	if err != nil {
		return fmt.Errorf("sshkey: invalid public key: %w", err)
	}
	fp := ssh.FingerprintSHA256(pk)

	keys, err := s.readAll()
	if err != nil {
		return err
	}
	for _, k := range keys {
		if k.Fingerprint == fp {
			return fmt.Errorf("sshkey: key already present (%s)", fp)
		}
	}

	// Normalise to a single line with the provided comment (or keep original).
	line := strings.TrimSpace(pubkey)
	if comment != "" {
		// Re-serialise with our comment.
		line = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
		// Replace trailing empty comment or add comment.
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			line = parts[0] + " " + parts[1] + " " + comment
		}
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return fmt.Errorf("sshkey: open authorized_keys: %w", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, line); err != nil {
		return fmt.Errorf("sshkey: write key: %w", err)
	}
	return nil
}

// Remove deletes the key matching the given SHA256 fingerprint.
// Returns an error if no matching key is found.
func (s *AuthStore) Remove(fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, err := s.readAll()
	if err != nil {
		return err
	}

	var kept []string
	found := false
	for _, k := range keys {
		if k.Fingerprint == fingerprint {
			found = true
			continue
		}
		kept = append(kept, k.PubKey)
	}
	if !found {
		return fmt.Errorf("sshkey: key not found (%s)", fingerprint)
	}

	content := strings.Join(kept, "\n")
	if len(kept) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(s.path, []byte(content), 0600); err != nil {
		return fmt.Errorf("sshkey: rewrite authorized_keys: %w", err)
	}
	return nil
}

// readAll parses the current authorized_keys file. Caller must hold s.mu.
func (s *AuthStore) readAll() ([]AuthorizedKey, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sshkey: read authorized_keys: %w", err)
	}

	var out []AuthorizedKey
	rest := data
	for len(rest) > 0 {
		var pk ssh.PublicKey
		var comment string
		pk, comment, _, rest, err = ssh.ParseAuthorizedKey(rest)
		if err != nil {
			break
		}
		fp := ssh.FingerprintSHA256(pk)
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
		// Re-attach comment if present.
		if comment != "" {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				line = parts[0] + " " + parts[1] + " " + comment
			}
		}
		out = append(out, AuthorizedKey{
			Comment:     comment,
			PubKey:      line,
			Fingerprint: fp,
		})
	}
	return out, nil
}

// fingerprintSHA256 returns a SHA256 fingerprint of raw bytes, in
// "SHA256:<base64>" format (mirrors ssh.FingerprintSHA256 for raw key bytes).
// Exported for test convenience.
func fingerprintSHA256(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("SHA256:%x", h)
}
