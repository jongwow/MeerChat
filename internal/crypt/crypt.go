// Package crypt implements MeerChat's room encryption.
//
// Every room is identified by a name and protected by a password. Both are
// known only to clients. From them a client derives:
//
//   - a route ID: an opaque hash of the room name that the server uses to
//     group connections. The server never sees the room name itself.
//   - a message key: Argon2id(password, salt(room)) expanded with HKDF. The
//     server never sees the password or the key.
//
// Messages are sealed with XChaCha20-Poly1305 under the message key, bound to
// the route ID as associated data, and padded to a fixed block size.
//
// Someone who joins with the wrong password derives a different key. They can
// still connect, but nothing they receive decrypts, and nothing they send
// decrypts for anyone else. Each side therefore only ever hears meerkats.
// That is "meerkat mode": it is not a feature the server implements, it is
// simply what failed decryption looks like.
package crypt

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// Version is the cryptographic suite version. It is mixed into every
// derivation so that a future suite can never be confused with this one.
const Version = 1

const (
	// RouteIDLen is the length in hex characters of a route ID.
	RouteIDLen = 32
	// PadBlock is the block size plaintexts are padded to before sealing,
	// so ciphertext length reveals message size only to this granularity.
	PadBlock = 128
	// MaxPlaintext is the largest plaintext Seal accepts. It leaves room for
	// a maximum-length message after JSON escaping, base64 and a signature.
	MaxPlaintext = 12 * 1024
	// MaxRoomName is the maximum room name length in bytes.
	MaxRoomName = 64

	nonceSize = chacha20poly1305.NonceSizeX
	keySize   = chacha20poly1305.KeySize
	lenPrefix = 4
)

// Params are the Argon2id cost parameters.
type Params struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// DefaultParams follow the RFC 9106 "second recommended" profile, scaled to
// 64 MiB so that joining a room stays well under a second on a laptop.
var DefaultParams = Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 4}

// TestParams are deliberately weak. Use them only in tests.
var TestParams = Params{Time: 1, MemoryKiB: 64, Threads: 1}

var (
	// ErrOpen is returned when a ciphertext cannot be authenticated, which
	// almost always means it was sealed under a different password.
	ErrOpen = errors.New("crypt: message authentication failed")
	// ErrTooLarge is returned when a plaintext exceeds MaxPlaintext.
	ErrTooLarge = errors.New("crypt: plaintext too large")
)

// ValidateRoomName reports whether name can be used as a room name.
func ValidateRoomName(name string) error {
	switch {
	case name == "":
		return errors.New("room name is empty")
	case len(name) > MaxRoomName:
		return fmt.Errorf("room name is longer than %d bytes", MaxRoomName)
	case !utf8.ValidString(name):
		return errors.New("room name is not valid UTF-8")
	case strings.TrimSpace(name) != name:
		return errors.New("room name has leading or trailing spaces")
	case strings.ContainsAny(name, "/?#\x00"):
		return errors.New("room name must not contain '/', '?', '#' or NUL")
	}
	return nil
}

func label(purpose string) string {
	return fmt.Sprintf("meerchat/v%d/%s", Version, purpose)
}

// RouteID returns the opaque identifier the server uses for a room.
// It is a domain-separated SHA-256 of the room name, truncated to 128 bits.
func RouteID(room string) string {
	h := sha256.New()
	h.Write([]byte(label("route")))
	h.Write([]byte{0})
	h.Write([]byte(room))
	return hex.EncodeToString(h.Sum(nil)[:RouteIDLen/2])
}

// ValidRouteID reports whether s looks like a route ID.
func ValidRouteID(s string) bool {
	if len(s) != RouteIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Key seals and opens messages for one room.
type Key struct {
	route       string
	aad         []byte
	key         []byte
	fingerprint string
}

// DeriveKey runs Argon2id over the password, salted by the room name, and
// expands the result into a message key and a short fingerprint.
func DeriveKey(room, password string, p Params) (*Key, error) {
	if err := ValidateRoomName(room); err != nil {
		return nil, err
	}
	salt := sha256.Sum256([]byte(label("salt") + "\x00" + room))
	master := argon2.IDKey([]byte(password), salt[:], p.Time, p.MemoryKiB, p.Threads, 32)
	defer clear(master)

	msgKey, err := hkdf.Key(sha256.New, master, nil, label("message-key"), keySize)
	if err != nil {
		return nil, err
	}
	fp, err := hkdf.Key(sha256.New, master, nil, label("fingerprint"), 4)
	if err != nil {
		return nil, err
	}
	route := RouteID(room)
	return &Key{
		route:       route,
		aad:         []byte(label("message") + "\x00" + route),
		key:         msgKey,
		fingerprint: fmt.Sprintf("%X-%X", fp[:2], fp[2:]),
	}, nil
}

// Route returns the route ID of the key's room.
func (k *Key) Route() string { return k.route }

// Fingerprint returns a short, human-comparable digest of the key. Two
// people in the same room see the same fingerprint if and only if (with
// overwhelming probability) they typed the same password. It is a sanity
// check, not an authentication mechanism: 32 bits is easy to collide.
func (k *Key) Fingerprint() string { return k.fingerprint }

// Seal pads and encrypts plaintext. The result is nonce || ciphertext.
func (k *Key) Seal(plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	aead, err := chacha20poly1305.NewX(k.key)
	if err != nil {
		return nil, err
	}
	padded := pad(plaintext)
	out := make([]byte, nonceSize, nonceSize+len(padded)+aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out, padded, k.aad), nil
}

// Open authenticates and decrypts a value produced by Seal.
func (k *Key) Open(sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(k.key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < nonceSize+aead.Overhead() {
		return nil, ErrOpen
	}
	padded, err := aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], k.aad)
	if err != nil {
		return nil, ErrOpen
	}
	return unpad(padded)
}

// pad prefixes the plaintext with its length and zero-fills to a multiple
// of PadBlock.
func pad(p []byte) []byte {
	n := lenPrefix + len(p)
	size := (n + PadBlock - 1) / PadBlock * PadBlock
	out := make([]byte, size)
	binary.BigEndian.PutUint32(out, uint32(len(p)))
	copy(out[lenPrefix:], p)
	return out
}

func unpad(b []byte) ([]byte, error) {
	if len(b) < lenPrefix || len(b)%PadBlock != 0 {
		return nil, ErrOpen
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(n) > uint64(len(b)-lenPrefix) {
		return nil, ErrOpen
	}
	return b[lenPrefix : lenPrefix+int(n)], nil
}
