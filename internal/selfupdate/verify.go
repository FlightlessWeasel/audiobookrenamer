package selfupdate

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
)

// embeddedPubKey is the release signing key that checksums.txt.sig is verified
// against.
//
// TODO: replace with the real release signing key before shipping. This is a
// throwaway P-256 key generated during development so the verify path is
// exercised end to end.
//
//go:embed keys/cosign.pub
var embeddedPubKey []byte

// verifyChecksums checks that sig is a valid signature over blob for the P-256
// public key in pubPEM. sig is the text that
// `cosign sign-blob --key cosign.key --output-signature` writes for a P-256 key:
// base64 of the ASN.1 DER ECDSA signature over the SHA-256 of the blob. This
// reimplements only that verification so the full sigstore/cosign dependency
// tree is not pulled in.
func verifyChecksums(sig, blob []byte, pubPEM []byte) error {
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	block, rest := pem.Decode(pubPEM)
	if block == nil {
		return fmt.Errorf("public key is not PEM")
	}
	if block.Type != "PUBLIC KEY" {
		return fmt.Errorf("public key PEM block is %q, want \"PUBLIC KEY\"", block.Type)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("public key PEM has %d bytes of trailing data", len(bytes.TrimSpace(rest)))
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}
	ecPub, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("public key is %T, want *ecdsa.PublicKey", pub)
	}

	sum := sha256.Sum256(blob)
	if !ecdsa.VerifyASN1(ecPub, sum[:], der) {
		return fmt.Errorf("checksums signature does not verify")
	}
	return nil
}
