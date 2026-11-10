// Package identity gives each device a signing key and remembers which key
// each nickname used, so impersonation inside a room becomes visible.
//
// The room password proves you belong in the room. It cannot say who you
// are: anyone with the password could claim any nickname. So every message
// is also signed with a per-device Ed25519 key, and receivers remember the
// first key they saw for each nickname in each room ("trust on first use",
// like SSH host keys). A later message under the same nickname from a
// different key is flagged.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Identity is a device's signing key.
type Identity struct {
	priv ed25519.PrivateKey
}

// Generate returns a new random identity.
func Generate() *Identity {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return &Identity{priv: priv}
}

const pemType = "MEER ED25519 PRIVATE KEY"

// LoadOrCreate reads the identity at path, creating it (mode 0600) if it
// does not exist.
func LoadOrCreate(path string) (*Identity, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the user's own key file
	if errors.Is(err, os.ErrNotExist) {
		id := Generate()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		data := pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: id.priv.Seed()})
		// O_EXCL: if two processes race to create it, one of them loses and
		// reads the other's key instead of overwriting it.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // see above
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreate(path)
		}
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return nil, err
		}
		return id, f.Close()
	}
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != pemType || len(blk.Bytes) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s is not a meer identity key", path)
	}
	return &Identity{priv: ed25519.NewKeyFromSeed(blk.Bytes)}, nil
}

// Public returns the public key.
func (id *Identity) Public() ed25519.PublicKey {
	return id.priv.Public().(ed25519.PublicKey)
}

// Sign signs msg.
func (id *Identity) Sign(msg []byte) []byte { return ed25519.Sign(id.priv, msg) }

// Fingerprint returns the identity's short fingerprint.
func (id *Identity) Fingerprint() string { return Fingerprint(id.Public()) }

// Fingerprint renders a public key as 64 bits of hex in four groups, which
// is plenty for a human comparison and short enough to read aloud.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:8])
	return strings.Join([]string{h[0:4], h[4:8], h[8:12], h[12:16]}, "-")
}

// Verify checks a signature by pub over msg.
func Verify(pub, msg, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && len(sig) == ed25519.SignatureSize &&
		ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}
