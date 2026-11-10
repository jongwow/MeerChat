package store

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// conformance runs the same behavioural checks against every Store.
func conformance(t *testing.T, open func(t *testing.T, perRoom int) Store) {
	ctx := context.Background()

	t.Run("sequence", func(t *testing.T) {
		s := open(t, 10)
		for i := 1; i <= 3; i++ {
			m, err := s.Append(ctx, "r", "p", int64(i), []byte{byte(i)})
			if err != nil {
				t.Fatal(err)
			}
			if m.Seq != uint64(i) {
				t.Fatalf("seq = %d, want %d", m.Seq, i)
			}
		}
		if _, err := s.Append(ctx, "other", "p", 1, []byte{1}); err != nil {
			t.Fatal(err)
		}
		if h, _ := s.Head(ctx, "r"); h != 3 {
			t.Fatalf("head = %d", h)
		}
		if h, _ := s.Head(ctx, "nope"); h != 0 {
			t.Fatalf("head of unknown room = %d", h)
		}
	})

	t.Run("since", func(t *testing.T) {
		s := open(t, 100)
		for i := 1; i <= 20; i++ {
			_, _ = s.Append(ctx, "r", "p", int64(i), []byte{byte(i)})
		}
		got, err := s.Since(ctx, "r", 15, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 5 || got[0].Seq != 16 || got[4].Seq != 20 {
			t.Fatalf("since 15: %+v", seqs(got))
		}
		got, _ = s.Since(ctx, "r", 0, 3)
		if len(got) != 3 || got[0].Seq != 18 {
			t.Fatalf("limit should keep the newest, oldest first: %v", seqs(got))
		}
		if got[2].CT[0] != 20 || got[2].Peer != "p" || got[2].TS != 20 {
			t.Fatalf("fields lost: %+v", got[2])
		}
	})

	t.Run("cap", func(t *testing.T) {
		s := open(t, 5)
		for i := 1; i <= 12; i++ {
			_, _ = s.Append(ctx, "r", "p", int64(i), []byte{1})
		}
		got, _ := s.Since(ctx, "r", 0, 100)
		if len(got) != 5 || got[0].Seq != 8 {
			t.Fatalf("cap not enforced: %v", seqs(got))
		}
	})

	t.Run("ephemeral", func(t *testing.T) {
		s := open(t, 0)
		m, _ := s.Append(ctx, "r", "p", 1, []byte{1})
		if m.Seq != 1 {
			t.Fatal("seq still assigned without history")
		}
		got, _ := s.Since(ctx, "r", 0, 100)
		if len(got) != 0 {
			t.Fatal("ephemeral store kept history")
		}
	})

	t.Run("prune keeps head", func(t *testing.T) {
		s := open(t, 100)
		now := time.Now()
		_, _ = s.Append(ctx, "r", "p", now.Add(-2*time.Hour).UnixMilli(), []byte{1})
		_, _ = s.Append(ctx, "r", "p", now.UnixMilli(), []byte{2})
		n, err := s.Prune(ctx, now.Add(-time.Hour))
		if err != nil || n != 1 {
			t.Fatalf("pruned %d, %v", n, err)
		}
		got, _ := s.Since(ctx, "r", 0, 100)
		if len(got) != 1 || got[0].Seq != 2 {
			t.Fatalf("after prune: %v", seqs(got))
		}
		_, _ = s.Prune(ctx, now.Add(time.Hour))
		m, _ := s.Append(ctx, "r", "p", now.UnixMilli(), []byte{3})
		if m.Seq != 3 {
			t.Fatalf("sequence regressed after prune: %d", m.Seq)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		s := open(t, 1000)
		var wg sync.WaitGroup
		for w := range 8 {
			wg.Go(func() {
				for i := range 25 {
					if _, err := s.Append(ctx, "r", fmt.Sprint(w), int64(i), []byte{1}); err != nil {
						t.Error(err)
					}
				}
			})
		}
		wg.Wait()
		got, _ := s.Since(ctx, "r", 0, 1000)
		if len(got) != 200 {
			t.Fatalf("got %d messages", len(got))
		}
		for i, m := range got {
			if m.Seq != uint64(i+1) {
				t.Fatalf("gap at %d: seq %d", i, m.Seq)
			}
		}
	})
}

func seqs(ms []Message) []uint64 {
	out := make([]uint64, len(ms))
	for i, m := range ms {
		out[i] = m.Seq
	}
	return out
}

func TestMemory(t *testing.T) {
	conformance(t, func(t *testing.T, n int) Store { return NewMemory(n) })
}

func TestSQLite(t *testing.T) {
	conformance(t, func(t *testing.T, n int) Store {
		s, err := OpenSQLite(filepath.Join(t.TempDir(), "meer.db"), n)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}

func TestSQLitePersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "meer.db")
	s, err := OpenSQLite(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.Append(ctx, "r", "p", 1, []byte("x"))
	_ = s.Close()

	s, err = OpenSQLite(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _ := s.Append(ctx, "r", "p", 2, []byte("y"))
	if m.Seq != 2 {
		t.Fatalf("seq after reopen = %d", m.Seq)
	}
}
