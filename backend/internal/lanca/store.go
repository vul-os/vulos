package lanca

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// File names inside a CA directory.
const (
	RootCertFile = "root.crt"
	RootKeyFile  = "root.key"
)

// Permissions. The key is 0600 in a 0700 directory; anything looser is treated
// as a leak, not a warning, because this key's whole value proposition is that
// it is harder to reach than a key sitting on the box.
const (
	caKeyMode os.FileMode = 0o600
	caDirMode os.FileMode = 0o700
)

// boxOnlyPathPrefixes are locations that belong to a RUNNING VULOS BOX. The CA
// key must never be written under any of them.
//
// This is the mechanical enforcement of the single most important property in
// this design: THE CA PRIVATE KEY DOES NOT LIVE ON THE BOX. A CA whose key
// sits on the machine it certifies is the weak form of this idea — whoever
// takes the box takes the authority to impersonate every name in the permitted
// subtrees, on every device the owner has installed the root on. The root's
// value comes entirely from being harder to reach than the thing it vouches
// for.
//
// `/var/lib/vulos` is the box data dir (datadir.Join's base, and the parent of
// lan.DefaultCertPath / lan.DefaultKeyPath). The literals are transcribed here
// rather than imported so that a refactor of the datadir package cannot quietly
// widen what this guard considers "on the box".
var boxOnlyPathPrefixes = []string{
	"/var/lib/vulos",
	"/etc/vulos",
	"/run/vulos",
}

// ErrKeyOnBox is returned when a caller tries to place CA key material in a
// location that belongs to a Vulos box.
type ErrKeyOnBox struct{ Path, Prefix string }

func (e *ErrKeyOnBox) Error() string {
	return fmt.Sprintf("lanca: REFUSING to write CA private key to %s: %s is a Vulos box path. "+
		"The CA key must live on an operator machine or control plane, never on the box it certifies — "+
		"a box that is stolen or rooted would otherwise hand over the authority to impersonate every name "+
		"in the permitted subtrees on every device that trusts this root", e.Path, e.Prefix)
}

// CheckNotOnBox reports whether dir is a location the CA key may be written to.
func CheckNotOnBox(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("lanca: resolve %q: %w", dir, err)
	}
	abs = filepath.Clean(abs)
	for _, p := range boxOnlyPathPrefixes {
		if abs == p || strings.HasPrefix(abs, p+string(filepath.Separator)) {
			return &ErrKeyOnBox{Path: abs, Prefix: p}
		}
	}
	return nil
}

// SaveRoot writes the root certificate and private key into dir.
//
// It refuses to overwrite an existing key: re-running `init` over a live CA
// would silently orphan every certificate already issued AND every root already
// installed on a device, which is a fleet-wide outage delivered by a typo.
func SaveRoot(dir string, r *Root) error {
	if err := CheckNotOnBox(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, caDirMode); err != nil {
		return fmt.Errorf("lanca: mkdir %s: %w", dir, err)
	}
	if err := os.Chmod(dir, caDirMode); err != nil {
		return fmt.Errorf("lanca: chmod %s: %w", dir, err)
	}

	keyPath := filepath.Join(dir, RootKeyFile)
	if _, err := os.Stat(keyPath); err == nil {
		return fmt.Errorf("lanca: %s already exists — refusing to overwrite a live CA key. "+
			"Every certificate issued by it and every device that already trusts it would be orphaned. "+
			"Move the directory aside deliberately if you really mean to start over", keyPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("lanca: stat %s: %w", keyPath, err)
	}

	ec, ok := r.Key.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("lanca: cannot persist a root key of type %T", r.Key)
	}
	der, err := x509.MarshalECPrivateKey(ec)
	if err != nil {
		return fmt.Errorf("lanca: marshal root key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})

	if err := os.WriteFile(keyPath, keyPEM, caKeyMode); err != nil {
		return fmt.Errorf("lanca: write %s: %w", keyPath, err)
	}
	// WriteFile only applies the mode on CREATE and passes it through umask, so
	// force it explicitly.
	if err := os.Chmod(keyPath, caKeyMode); err != nil {
		return fmt.Errorf("lanca: chmod %s: %w", keyPath, err)
	}

	certPath := filepath.Join(dir, RootCertFile)
	if err := os.WriteFile(certPath, r.CertPEM(), 0o644); err != nil {
		return fmt.Errorf("lanca: write %s: %w", certPath, err)
	}
	return nil
}

// OpenRoot loads a CA from dir, refusing to use a key whose file permissions
// let any other local user read it.
func OpenRoot(dir string) (*Root, error) {
	keyPath := filepath.Join(dir, RootKeyFile)
	certPath := filepath.Join(dir, RootCertFile)

	fi, err := os.Stat(keyPath)
	if err != nil {
		return nil, fmt.Errorf("lanca: no CA at %s (%w) — run `vulos-lanca init` first", dir, err)
	}
	// 0o077: any permission bit for group or other. A CA key readable by
	// another local account is a CA key that account can use.
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("lanca: CA private key %s is mode %v — it is readable by other local users, "+
			"which makes it their CA too. Fix with: chmod 600 %s", keyPath, perm, keyPath)
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("lanca: read %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("lanca: read %s: %w", keyPath, err)
	}
	return LoadRoot(certPEM, keyPEM)
}

// ParseCertPEM decodes a single PEM CERTIFICATE block. Exported so the operator
// tool can inspect any certificate — including one it did not issue — without
// reimplementing PEM handling.
func ParseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("lanca: input is not a PEM CERTIFICATE block")
	}
	return x509.ParseCertificate(block.Bytes)
}
