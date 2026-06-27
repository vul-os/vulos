package main

// files_peer_signer.go — adapts the peering box identity to the Files
// peer-share signer seam (files.PeerSigner). This is the ONLY glue that lets the
// files package issue/verify capabilities with the box's Ed25519 identity
// without importing the peering package itself (which would risk an import
// cycle and couple the control plane to the transport).

import (
	"crypto/ed25519"

	"vulos/backend/services/peering"
)

// peerShareSigner implements files.PeerSigner over the peering identity key.
type peerShareSigner struct {
	selfID string
	priv   ed25519.PrivateKey
}

func (s peerShareSigner) SelfID() string { return s.selfID }

func (s peerShareSigner) Sign(msg []byte) []byte { return ed25519.Sign(s.priv, msg) }

func (s peerShareSigner) Verify(vulaID string, msg, sig []byte) error {
	return peering.VerifyVulaSignature(vulaID, msg, sig)
}
