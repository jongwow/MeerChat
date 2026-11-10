package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Trust levels attached to each received message.
const (
	TrustSelf    = "self"    // sent by this device
	TrustNew     = "new"     // first key ever seen for this nickname here
	TrustKnown   = "known"   // same key as before
	TrustChanged = "changed" // this nickname used a different key before
)

// Book remembers, per room, which public key each nickname belongs to. It
// is safe for concurrent use.
type Book struct {
	path string // "" keeps everything in memory

	mu    sync.Mutex
	known map[string]map[string][]byte // room -> nick -> key
	last  map[string]map[string][]byte // room -> nick -> most recent key seen
}

// NewBook returns an in-memory book.
func NewBook() *Book {
	return &Book{known: map[string]map[string][]byte{}, last: map[string]map[string][]byte{}}
}

// OpenBook loads a book persisted at path, which need not exist yet.
func OpenBook(path string) (*Book, error) {
	b := NewBook()
	b.path = path
	data, err := os.ReadFile(path) //nolint:gosec // the user's own state file
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &b.known); err != nil {
		return nil, err
	}
	if b.known == nil {
		b.known = map[string]map[string][]byte{}
	}
	return b, nil
}

// Observe records that nick spoke in room with key pub and says how much
// to trust it. The first key seen for a nickname is remembered; a different
// key later is reported as changed but does not replace it.
func (b *Book) Observe(room, nick string, pub []byte) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	setIn(b.last, room, nick, pub)
	prev, ok := b.known[room][nick]
	switch {
	case !ok:
		setIn(b.known, room, nick, pub)
		b.saveLocked()
		return TrustNew
	case bytes.Equal(prev, pub):
		return TrustKnown
	default:
		return TrustChanged
	}
}

// Rename handles a nickname change signed by pub: if old belonged to pub,
// new does too (unless new is already someone else's).
func (b *Book) Rename(room, oldNick, newNick string, pub []byte) string {
	b.mu.Lock()
	prev, ok := b.known[room][oldNick]
	b.mu.Unlock()
	if ok && !bytes.Equal(prev, pub) {
		return TrustChanged
	}
	return b.Observe(room, newNick, pub)
}

// Accept trusts the most recent key seen for nick in room from now on.
func (b *Book) Accept(room, nick string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pub, ok := b.last[room][nick]
	if !ok {
		return nil, errors.New("haven't seen " + nick + " say anything in this room yet")
	}
	setIn(b.known, room, nick, pub)
	b.saveLocked()
	return pub, nil
}

// Known returns the remembered key for nick in room.
func (b *Book) Known(room, nick string) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pub, ok := b.known[room][nick]
	return pub, ok
}

func setIn(m map[string]map[string][]byte, room, nick string, pub []byte) {
	if m[room] == nil {
		m[room] = map[string][]byte{}
	}
	m[room][nick] = append([]byte(nil), pub...)
}

// saveLocked writes the book atomically. Errors are ignored: losing the
// book only means seeing "new" again, never accepting a wrong key.
func (b *Book) saveLocked() {
	if b.path == "" {
		return
	}
	data, err := json.MarshalIndent(b.known, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return
	}
	tmp := b.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, b.path)
}
