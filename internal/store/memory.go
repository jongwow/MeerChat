package store

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-memory Store that keeps at most PerRoom messages per room.
// With PerRoom == 0 it stores nothing and only hands out sequence numbers,
// which makes rooms fully ephemeral.
type Memory struct {
	perRoom int

	mu    sync.Mutex
	rooms map[string]*memLog
}

type memLog struct {
	head uint64
	msgs []Message // ring buffer, oldest first after normalize
}

// NewMemory returns a Memory store.
func NewMemory(perRoom int) *Memory {
	return &Memory{perRoom: max(perRoom, 0), rooms: make(map[string]*memLog)}
}

func (m *Memory) log(route string) *memLog {
	l, ok := m.rooms[route]
	if !ok {
		l = &memLog{}
		m.rooms[route] = l
	}
	return l
}

func (m *Memory) Append(_ context.Context, route, peer string, ts int64, ct []byte) (Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.log(route)
	l.head++
	msg := Message{Seq: l.head, Peer: peer, TS: ts, CT: ct}
	if m.perRoom > 0 {
		l.msgs = append(l.msgs, msg)
		if over := len(l.msgs) - m.perRoom; over > 0 {
			l.msgs = append(l.msgs[:0:0], l.msgs[over:]...)
		}
	}
	return msg, nil
}

func (m *Memory) Since(_ context.Context, route string, after uint64, limit int) ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.rooms[route]
	if !ok || limit <= 0 {
		return nil, nil
	}
	// msgs is sorted by Seq; find the first index with Seq > after.
	i := len(l.msgs)
	for i > 0 && l.msgs[i-1].Seq > after {
		i--
	}
	out := l.msgs[i:]
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return append([]Message(nil), out...), nil
}

func (m *Memory) Head(_ context.Context, route string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if l, ok := m.rooms[route]; ok {
		return l.head, nil
	}
	return 0, nil
}

func (m *Memory) Prune(_ context.Context, cutoff time.Time) (int, error) {
	c := cutoff.UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, l := range m.rooms {
		i := 0
		for i < len(l.msgs) && l.msgs[i].TS < c {
			i++
		}
		n += i
		l.msgs = append(l.msgs[:0:0], l.msgs[i:]...)
		// The head is kept even when a room empties so sequence numbers stay
		// monotonic. It costs a few dozen bytes per room ever used.
	}
	return n, nil
}

func (m *Memory) Close() error { return nil }
