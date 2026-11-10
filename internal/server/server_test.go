package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/store"
)

var route = crypt.RouteID("lobby")

func newTestServer(t testing.TB, cfg Config) (*Server, string) {
	t.Helper()
	s := New(cfg)
	ts := httptest.NewServer(s)
	t.Cleanup(func() {
		s.Close()
		ts.Close()
	})
	return s, "ws" + strings.TrimPrefix(ts.URL, "http")
}

type testConn struct {
	t  testing.TB
	c  *websocket.Conn
	id string
}

func dial(t testing.TB, base, route string, since uint64) *testConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, fmt.Sprintf("%s/v1/rooms/%s/ws?since=%d", base, route, since),
		&websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	c.SetReadLimit(protocol.MaxFrame)
	tc := &testConn{t: t, c: c}
	t.Cleanup(func() { c.CloseNow() })
	w := tc.next()
	if w.Type != protocol.TypeWelcome {
		t.Fatalf("first frame %q", w.Type)
	}
	tc.id = w.You
	return tc
}

func (tc *testConn) next() protocol.ServerFrame {
	tc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := tc.c.Read(ctx)
	if err != nil {
		tc.t.Fatal(err)
	}
	var f protocol.ServerFrame
	if err := json.Unmarshal(b, &f); err != nil {
		tc.t.Fatal(err)
	}
	return f
}

// nextOf skips frames until one of type typ arrives.
func (tc *testConn) nextOf(typ string) protocol.ServerFrame {
	tc.t.Helper()
	for {
		if f := tc.next(); f.Type == typ {
			return f
		}
	}
}

func (tc *testConn) send(ct string) {
	tc.t.Helper()
	b, _ := json.Marshal(protocol.ClientFrame{Type: protocol.TypeSend, CT: []byte(ct)})
	if err := tc.c.Write(context.Background(), websocket.MessageText, b); err != nil {
		tc.t.Fatal(err)
	}
}

func TestRelay(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := dial(t, url, route, 0)
	b := dial(t, url, route, 0)
	if p := a.nextOf(protocol.TypePresence); p.Event != protocol.PresenceJoin || p.Peer != b.id || p.Peers != 2 {
		t.Fatalf("presence %+v", p)
	}

	a.send("hello")
	for _, c := range []*testConn{a, b} {
		m := c.nextOf(protocol.TypeMessage)
		if string(m.CT) != "hello" || m.Peer != a.id || m.Seq != 1 || m.TS == 0 {
			t.Fatalf("got %+v", m)
		}
	}
}

func TestRoomsAreIsolated(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := dial(t, url, route, 0)
	other := dial(t, url, crypt.RouteID("elsewhere"), 0)
	a.send("x")
	a.nextOf(protocol.TypeMessage)
	other.send("y")
	if m := other.nextOf(protocol.TypeMessage); string(m.CT) != "y" {
		t.Fatalf("cross-room leak: %+v", m)
	}
}

func TestHistoryReplay(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := dial(t, url, route, 0)
	for i := range 5 {
		a.send(fmt.Sprint(i))
		a.nextOf(protocol.TypeMessage)
	}
	// Fresh client gets everything.
	b := dial(t, url, route, 0)
	for i := range 5 {
		if m := b.nextOf(protocol.TypeMessage); string(m.CT) != fmt.Sprint(i) {
			t.Fatalf("replay %d: %q", i, m.CT)
		}
	}
	// Reconnecting client gets only what it missed.
	c := dial(t, url, route, 3)
	if m := c.nextOf(protocol.TypeMessage); m.Seq != 4 {
		t.Fatalf("resume got seq %d", m.Seq)
	}
	// A client ahead of the relay (relay lost history) gets everything.
	d := dial(t, url, route, 99)
	if m := d.nextOf(protocol.TypeMessage); m.Seq != 1 {
		t.Fatalf("expected full replay, got seq %d", m.Seq)
	}
}

func TestWelcomeHead(t *testing.T) {
	_, url := newTestServer(t, Config{})
	a := dial(t, url, route, 0)
	a.send("x")
	a.nextOf(protocol.TypeMessage)

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url+"/v1/rooms/"+route+"/ws", &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, b, _ := c.Read(ctx)
	var w protocol.ServerFrame
	_ = json.Unmarshal(b, &w)
	if w.Head != 1 || w.Peers != 2 {
		t.Fatalf("welcome %+v", w)
	}
}

func TestBadInput(t *testing.T) {
	_, url := newTestServer(t, Config{})
	resp, err := http.Get(strings.Replace(url, "ws", "http", 1) + "/v1/rooms/not-a-route/ws")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}

	a := dial(t, url, route, 0)
	_ = a.c.Write(context.Background(), websocket.MessageText, []byte(`{"t":"bogus"}`))
	if e := a.nextOf(protocol.TypeError); e.Code != protocol.ErrBadFrame {
		t.Fatalf("got %+v", e)
	}
	// Connection survives a bad frame.
	a.send("ok")
	a.nextOf(protocol.TypeMessage)
}

func TestSubprotocolRequired(t *testing.T) {
	_, url := newTestServer(t, Config{})
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url+"/v1/rooms/"+route+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("expected policy violation, got %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	_, url := newTestServer(t, Config{Rate: 1, Burst: 3})
	a := dial(t, url, route, 0)
	for range 5 {
		a.send("spam")
	}
	var msgs, limited int
	for msgs+limited < 5 {
		switch f := a.next(); f.Type {
		case protocol.TypeMessage:
			msgs++
		case protocol.TypeError:
			if f.Code == protocol.ErrRateLimited {
				limited++
			}
		}
	}
	if msgs != 3 || limited != 2 {
		t.Fatalf("msgs=%d limited=%d", msgs, limited)
	}
}

func TestFanoutKicksFullQueues(t *testing.T) {
	kicked := make(chan string, 2)
	mk := func(id string, buf int) *peer {
		return &peer{id: id, out: make(chan []byte, buf), close: func(_ websocket.StatusCode, reason string) {
			kicked <- id + ": " + reason
		}}
	}
	fast, slow := mk("fast", 10), mk("slow", 1)
	s := New(Config{})
	defer s.Close()
	rm := &room{peers: map[*peer]struct{}{fast: {}, slow: {}}}
	rm.mu.Lock()
	s.fanout(rm, nil, []byte("1"))
	s.fanout(rm, nil, []byte("2")) // slow's queue is full now
	s.fanout(rm, nil, []byte("3")) // kicked once, not twice
	rm.mu.Unlock()
	select {
	case k := <-kicked:
		if k != "slow: too slow" {
			t.Fatalf("kicked %q", k)
		}
	case <-time.After(time.Second):
		t.Fatal("slow peer not kicked")
	}
	select {
	case k := <-kicked:
		t.Fatalf("unexpected second kick %q", k)
	case <-time.After(50 * time.Millisecond):
	}
	if len(fast.out) != 3 {
		t.Fatalf("fast peer got %d frames", len(fast.out))
	}
}

func TestSlowConsumerIsDropped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows auto-tunes socket buffers far beyond what this test can fill; TestFanoutKicksFullQueues covers the logic")
	}
	_, url := newTestServer(t, Config{SendBuffer: 4, Replay: 1, Rate: 1e6, Burst: 1e6, Store: store.NewMemory(0)})
	fast := dial(t, url, route, 0)
	dial(t, url, route, 0) // never reads

	fast.nextOf(protocol.TypePresence)
	big := strings.Repeat("x", protocol.MaxCiphertext)
	sawLeave := false
	// Large frames fill the kernel socket buffers quickly; after that the
	// slow peer's queue overflows and the server must cut it loose.
	for i := 0; i < 5000 && !sawLeave; i++ {
		fast.send(big)
		for {
			f := fast.next()
			if f.Type == protocol.TypePresence && f.Event == protocol.PresenceLeave {
				sawLeave = true
			}
			if f.Type == protocol.TypeMessage || f.Type == protocol.TypeError {
				break
			}
		}
	}
	if !sawLeave {
		t.Fatal("slow consumer was never dropped")
	}
}

func TestRoomCleanup(t *testing.T) {
	s, url := newTestServer(t, Config{})
	a := dial(t, url, route, 0)
	if s.Stats().Rooms != 1 {
		t.Fatal("room not created")
	}
	a.c.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(2 * time.Second)
	for s.Stats().Rooms != 0 {
		if time.Now().After(deadline) {
			t.Fatal("empty room not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRoomFull(t *testing.T) {
	_, url := newTestServer(t, Config{MaxPeersPerRoom: 1})
	dial(t, url, route, 0)
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, url+"/v1/rooms/"+route+"/ws", &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, _, err = c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Fatalf("got %v", err)
	}
}

func TestShutdownClosesPeers(t *testing.T) {
	s := New(Config{})
	ts := httptest.NewServer(s)
	defer ts.Close()
	a := dial(t, "ws"+strings.TrimPrefix(ts.URL, "http"), route, 0)
	s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := a.c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusGoingAway {
				t.Fatalf("got %v", err)
			}
			return
		}
	}
}

func TestOrderingUnderConcurrency(t *testing.T) {
	_, url := newTestServer(t, Config{Rate: 1e6, Burst: 1e6, SendBuffer: 4096})
	const senders, each = 8, 50
	obs := dial(t, url, route, 0)
	conns := make([]*testConn, senders)
	for i := range conns {
		conns[i] = dial(t, url, route, 0)
	}
	var wg sync.WaitGroup
	for _, c := range conns {
		wg.Go(func() {
			for range each {
				b, _ := json.Marshal(protocol.ClientFrame{Type: protocol.TypeSend, CT: []byte("x")})
				_ = c.c.Write(context.Background(), websocket.MessageText, b)
			}
		})
	}
	wg.Wait()
	var last uint64
	for range senders * each {
		m := obs.nextOf(protocol.TypeMessage)
		if m.Seq != last+1 {
			t.Fatalf("out of order: %d after %d", m.Seq, last)
		}
		last = m.Seq
	}
}

func TestSQLiteBackedServer(t *testing.T) {
	st, err := store.OpenSQLite(t.TempDir()+"/m.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, url := newTestServer(t, Config{Store: st})
	a := dial(t, url, route, 0)
	a.send("persisted")
	a.nextOf(protocol.TypeMessage)
	b := dial(t, url, route, 0)
	if m := b.nextOf(protocol.TypeMessage); string(m.CT) != "persisted" {
		t.Fatal("history not served from sqlite")
	}
}

// BenchmarkFanout measures relaying one message to a room of N listeners.
func BenchmarkFanout(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("peers=%d", n), func(b *testing.B) {
			_, url := newTestServer(b, Config{Rate: 1e9, Burst: 1e9, SendBuffer: 1 << 14, Replay: 1, MaxPeersPerRoom: 1000, Store: store.NewMemory(0)})
			r := crypt.RouteID(fmt.Sprint("bench", n))
			sender := dial(b, url, r, 0)
			var wg sync.WaitGroup
			listeners := make([]*testConn, n)
			for i := range listeners {
				listeners[i] = dial(b, url, r, 0)
			}
			// Drain presence noise before timing.
			time.Sleep(200 * time.Millisecond)
			b.ResetTimer()
			for _, l := range listeners {
				wg.Go(func() {
					got := 0
					for got < b.N {
						_, data, err := l.c.Read(context.Background())
						if err != nil {
							return
						}
						if strings.Contains(string(data), `"t":"msg"`) {
							got++
						}
					}
				})
			}
			msg, _ := json.Marshal(protocol.ClientFrame{Type: protocol.TypeSend, CT: make([]byte, 256)})
			for range b.N {
				_ = sender.c.Write(context.Background(), websocket.MessageText, msg)
			}
			wg.Wait()
			b.ReportMetric(float64(b.N*n)/b.Elapsed().Seconds(), "deliveries/s")
		})
	}
}
