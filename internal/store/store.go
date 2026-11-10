// Package store persists room history for the server.
//
// The server only ever has ciphertext, so that is all a store holds: a
// sequence number, an ephemeral peer ID, a timestamp and opaque bytes.
// Losing the database to an attacker reveals traffic patterns, not content.
package store

import (
	"context"
	"time"
)

// Message is one stored ciphertext.
type Message struct {
	Seq  uint64
	Peer string
	TS   int64 // unix milliseconds
	CT   []byte
}

// Store is the server's history backend. Implementations must be safe for
// concurrent use. Sequence numbers are per route, start at 1, and never go
// backwards for the lifetime of the store.
type Store interface {
	// Append stores a message and assigns it the next sequence number.
	Append(ctx context.Context, route, peer string, ts int64, ct []byte) (Message, error)
	// Since returns up to limit of the most recent messages with Seq > after,
	// oldest first.
	Since(ctx context.Context, route string, after uint64, limit int) ([]Message, error)
	// Head returns the latest sequence number for route, or 0.
	Head(ctx context.Context, route string) (uint64, error)
	// Prune deletes messages older than cutoff.
	Prune(ctx context.Context, cutoff time.Time) (int, error)
	Close() error
}
