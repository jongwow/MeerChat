package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/identity"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/server"
	"github.com/jongwow/meerchat/internal/store"
)

// relay is a test relay that can be stopped and restarted on the same port.
type relay struct {
	t     *testing.T
	addr  string
	store store.Store
	srv   *server.Server
	http  *http.Server
}

func startRelay(t *testing.T) *relay {
	r := &relay{t: t, store: store.NewMemory(100)}
	r.start("127.0.0.1:0")
	t.Cleanup(r.stop)
	return r
}

func (r *relay) start(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		r.t.Fatal(err)
	}
	r.addr = ln.Addr().String()
	r.srv = server.New(server.Config{Store: r.store, PingInterval: time.Second})
	r.http = &http.Server{Handler: r.srv}
	go func() { _ = r.http.Serve(ln) }()
}

func (r *relay) stop() {
	if r.http != nil {
		_ = r.http.Close()
		r.srv.Close()
		r.http = nil
	}
}

func (r *relay) room(name string) string { return "meer://" + r.addr + "/" + name }

func newManager(t *testing.T, nick string) *Manager {
	m, err := NewManager(Options{Nick: nick, Params: crypt.TestParams})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

type watcher struct {
	t  *testing.T
	ch <-chan chat.Event
}

func watch(t *testing.T, m *Manager) *watcher {
	ch, cancel := m.Subscribe(0)
	t.Cleanup(cancel)
	return &watcher{t: t, ch: ch}
}

// waitFor returns the first event satisfying pred.
func (w *watcher) waitFor(desc string, pred func(chat.Event) bool) chat.Event {
	w.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				w.t.Fatalf("subscription closed while waiting for %s", desc)
			}
			if pred(e) {
				return e
			}
		case <-timeout:
			w.t.Fatalf("timed out waiting for %s", desc)
		}
	}
}

func kind(k string) func(chat.Event) bool { return func(e chat.Event) bool { return e.Kind == k } }

func TestEndToEnd(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	aw, bw := watch(t, alice), watch(t, bob)

	ai, err := alice.Join(ctx, r.room("lobby"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	// Once alice sees her own announcement, the relay has stored it.
	aw.waitFor("alice's own join", kind(chat.KindJoin))
	bi, err := bob.Join(ctx, r.room("lobby"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	if ai.Fingerprint != bi.Fingerprint {
		t.Fatal("same password, different fingerprint")
	}
	// Bob sees alice's join announcement replayed from history.
	j := bw.waitFor("alice join", func(e chat.Event) bool { return e.Kind == chat.KindJoin && e.Nick == "alice" })
	if !j.History {
		t.Error("alice's join should be marked as history for bob")
	}

	if err := alice.Send(ctx, "", chat.KindMessage, "hello bob"); err != nil {
		t.Fatal(err)
	}
	e := bw.waitFor("message", kind(chat.KindMessage))
	if e.Nick != "alice" || e.Text != "hello bob" || e.Self || e.History {
		t.Fatalf("got %+v", e)
	}

	if err := alice.SetNick(ctx, "alicia"); err != nil {
		t.Fatal(err)
	}
	n := bw.waitFor("nick", kind(chat.KindNick))
	if n.Nick != "alicia" || n.Text != "alice" {
		t.Fatalf("nick event %+v", n)
	}

	if err := alice.Send(ctx, "lobby", chat.KindAction, "waves"); err != nil {
		t.Fatal(err)
	}
	if a := bw.waitFor("action", kind(chat.KindAction)); a.Text != "waves" {
		t.Fatalf("got %+v", a)
	}

	if err := alice.Leave(ctx, "lobby"); err != nil {
		t.Fatal(err)
	}
	if l := bw.waitFor("leave", kind(chat.KindLeave)); l.Nick != "alicia" {
		t.Fatalf("got %+v", l)
	}
}

func TestSelfMessages(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice := newManager(t, "alice")
	aw := watch(t, alice)
	if _, err := alice.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}
	_ = alice.Send(ctx, "", chat.KindMessage, "echo")
	if e := aw.waitFor("own message", kind(chat.KindMessage)); !e.Self {
		t.Fatal("own message not marked self")
	}
}

func TestWrongPasswordHearsMeerkats(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, mallory := newManager(t, "alice"), newManager(t, "mallory")
	aw, mw := watch(t, alice), watch(t, mallory)

	ai, _ := alice.Join(ctx, r.room("secret"), "correct")
	mi, err := mallory.Join(ctx, r.room("secret"), "guess")
	if err != nil {
		t.Fatal(err)
	}
	if ai.Fingerprint == mi.Fingerprint {
		t.Fatal("fingerprints should differ")
	}

	_ = alice.Send(ctx, "", chat.KindMessage, "the plans are in the shoebox")
	e := mw.waitFor("meerkat", func(e chat.Event) bool { return e.Kind == chat.KindMeerkat && !e.History })
	if strings.Contains(e.Text, "shoebox") || !strings.HasPrefix(e.Nick, "meerkat-") {
		t.Fatalf("leak: %+v", e)
	}

	// And the other way round: alice hears mallory as a meerkat too.
	_ = mallory.Send(ctx, "", chat.KindMessage, "hello?")
	aw.waitFor("meerkat from mallory", func(e chat.Event) bool { return e.Kind == chat.KindMeerkat && e.Peer != "" })
}

func TestReconnectResumes(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	bw := watch(t, bob)
	_, _ = alice.Join(ctx, r.room("lobby"), "pw")
	_, _ = bob.Join(ctx, r.room("lobby"), "pw")
	_ = alice.Send(ctx, "", chat.KindMessage, "before")
	bw.waitFor("before", func(e chat.Event) bool { return e.Text == "before" })

	addr := r.addr
	r.stop()
	bw.waitFor("offline", func(e chat.Event) bool { return e.Kind == chat.KindStatus && e.Status == chat.StatusOffline })
	r.start(addr) // same store: history survives, as with --db

	bw.waitFor("online again", func(e chat.Event) bool { return e.Kind == chat.KindStatus && e.Status == chat.StatusOnline })
	if err := alice.Send(ctx, "", chat.KindMessage, "after"); err != nil {
		t.Fatal(err)
	}
	got := bw.waitFor("after", func(e chat.Event) bool { return e.Kind == chat.KindMessage })
	if got.Text != "after" {
		t.Fatalf("expected only the new message, got %q (duplicate replay?)", got.Text)
	}
}

func TestRelayLosesHistory(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	bw := watch(t, bob)
	_, _ = alice.Join(ctx, r.room("lobby"), "pw")
	_, _ = bob.Join(ctx, r.room("lobby"), "pw")
	for range 3 {
		_ = alice.Send(ctx, "", chat.KindMessage, "old")
		bw.waitFor("old", kind(chat.KindMessage))
	}

	addr := r.addr
	r.stop()
	r.store = store.NewMemory(100) // relay restarted without a database
	r.start(addr)

	// Sent while alice may still be reconnecting: the outbox delivers it.
	if err := alice.Send(ctx, "", chat.KindMessage, "fresh"); err != nil {
		t.Fatal(err)
	}
	// The new message has seq 1, lower than what bob has seen. It must not
	// be discarded as a duplicate.
	bw.waitFor("fresh", func(e chat.Event) bool { return e.Text == "fresh" })
}

func TestJoinErrors(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	m := newManager(t, "alice")
	if _, err := m.Join(ctx, "meer://127.0.0.1:1/lobby", "pw"); err == nil {
		t.Fatal("joined an unreachable relay")
	}
	if len(m.Rooms()) != 0 {
		t.Fatal("failed join left a room behind")
	}
	if _, err := m.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Join(ctx, r.room("lobby"), "pw"); !errors.Is(err, ErrAlreadyJoined) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.Join(ctx, "http://nope/x", "pw"); err == nil {
		t.Fatal("bad address accepted")
	}
}

func TestResolve(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	m := newManager(t, "alice")
	if _, err := m.Resolve(""); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("got %v", err)
	}
	_, _ = m.Join(ctx, r.room("one"), "pw")
	_, _ = m.Join(ctx, r.room("two"), "pw")
	if u, _ := m.Resolve(""); !strings.HasSuffix(u, "/two") {
		t.Fatalf("default room %q", u)
	}
	if u, _ := m.Resolve("one"); !strings.HasSuffix(u, "/one") {
		t.Fatalf("by name %q", u)
	}
	if u, _ := m.Resolve(r.room("one")); !strings.HasSuffix(u, "/one") {
		t.Fatalf("by url %q", u)
	}
	if _, err := m.Resolve("three"); err == nil {
		t.Fatal("resolved unknown room")
	}
}

func TestSubscribeBacklog(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	m := newManager(t, "alice")
	_, _ = m.Join(ctx, r.room("lobby"), "pw")
	for range 3 {
		_ = m.Send(ctx, "", chat.KindMessage, "x")
	}
	w := watch(t, m)
	w.waitFor("live", kind(chat.KindMessage)) // drain the 3 live ones via a fresh subscription below
	ch, cancel := m.Subscribe(100)
	defer cancel()
	n := 0
	for n < 3 {
		select {
		case e := <-ch:
			if e.Kind == chat.KindMessage {
				n++
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("backlog delivered %d messages", n)
		}
	}
}

func TestAllowAddress(t *testing.T) {
	m, _ := NewManager(Options{Params: crypt.TestParams, AllowAddress: func(a chat.Address) error {
		return context.Canceled
	}})
	defer m.Close()
	if _, err := m.Join(context.Background(), "meer://elsewhere/x", "pw"); err == nil {
		t.Fatal("AllowAddress ignored")
	}
}

func TestDedupe(t *testing.T) {
	d := newDedupe(2)
	if !d.add("a") || d.add("a") || !d.add("b") || !d.add("c") || !d.add("a") {
		t.Fatal("dedupe ring misbehaves")
	}
}

func TestOutboxDeliversAfterOutage(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	aw, bw := watch(t, alice), watch(t, bob)
	_, _ = alice.Join(ctx, r.room("lobby"), "pw")
	_, _ = bob.Join(ctx, r.room("lobby"), "pw")

	addr := r.addr
	r.stop()
	aw.waitFor("alice offline", func(e chat.Event) bool { return e.Kind == chat.KindStatus && e.Status == chat.StatusOffline })
	for i := range 3 {
		if err := alice.Send(ctx, "", chat.KindMessage, fmt.Sprint("queued ", i)); err != nil {
			t.Fatalf("send while offline: %v", err)
		}
	}
	r.start(addr)
	for i := range 3 {
		want := fmt.Sprint("queued ", i)
		bw.waitFor(want, func(e chat.Event) bool { return e.Kind == chat.KindMessage && e.Text == want })
	}
	// Exactly once on screen, even though the relay may have stored a
	// message twice.
	_ = alice.Send(ctx, "", chat.KindMessage, "marker")
	bw.waitFor("marker", func(e chat.Event) bool {
		if e.Kind == chat.KindMessage && strings.HasPrefix(e.Text, "queued") {
			t.Fatalf("duplicate %q", e.Text)
		}
		return e.Text == "marker"
	})
}

func TestImpersonationIsFlagged(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	mallory := newManager(t, "alice") // knows the password, borrows the name
	bw := watch(t, bob)
	for _, m := range []*Manager{alice, bob} {
		if _, err := m.Join(ctx, r.room("lobby"), "pw"); err != nil {
			t.Fatal(err)
		}
	}
	_ = alice.Send(ctx, "", chat.KindMessage, "real alice here")
	real := bw.waitFor("real alice", func(e chat.Event) bool { return e.Text == "real alice here" })
	// Her join announcement was first contact, so this is already "known".
	if real.Trust != identity.TrustKnown || real.Key != alice.Identity() {
		t.Fatalf("first contact: %+v", real)
	}

	if _, err := mallory.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}
	_ = mallory.Send(ctx, "", chat.KindMessage, "send me the shoebox")
	fake := bw.waitFor("fake alice", func(e chat.Event) bool { return e.Text == "send me the shoebox" })
	if fake.Nick != "alice" || fake.Trust != identity.TrustChanged || fake.Key != mallory.Identity() {
		t.Fatalf("impersonation not flagged: %+v", fake)
	}

	// The real alice is still recognised.
	_ = alice.Send(ctx, "", chat.KindMessage, "that wasn't me")
	if e := bw.waitFor("alice again", func(e chat.Event) bool { return e.Text == "that wasn't me" }); e.Trust != identity.TrustKnown {
		t.Fatalf("real alice after impostor: %+v", e)
	}

	// bob can explicitly accept a new key.
	_ = mallory.Send(ctx, "", chat.KindMessage, "new laptop, honest")
	bw.waitFor("again", func(e chat.Event) bool { return e.Text == "new laptop, honest" })
	if fp, err := bob.Trust("lobby", "alice"); err != nil || fp != mallory.Identity() {
		t.Fatalf("trust: %s %v", fp, err)
	}
	_ = mallory.Send(ctx, "", chat.KindMessage, "thanks")
	if e := bw.waitFor("trusted", func(e chat.Event) bool { return e.Text == "thanks" }); e.Trust != identity.TrustKnown {
		t.Fatalf("after trust: %+v", e)
	}
}

func TestForgedSignatureIsNoise(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	bob := newManager(t, "bob")
	bw := watch(t, bob)
	if _, err := bob.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}

	// Someone with the password signs with one key and claims another.
	key, _ := crypt.DeriveKey("lobby", "pw", crypt.TestParams)
	body, _ := json.Marshal(protocol.NewPayload(protocol.KindMessage, "alice", "forged"))
	signer, claimed := identity.Generate(), identity.Generate()
	pt, _ := json.Marshal(protocol.Envelope{Body: body, Key: claimed.Public(), Sig: signer.Sign(protocol.SigningInput(key.Route(), body))})
	ct, _ := key.Seal(pt)

	addr, _ := chat.ParseAddress(r.room("lobby"))
	c, _, err := websocket.Dial(ctx, addr.WebSocketURL(key.Route()), &websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	frame, _ := json.Marshal(protocol.ClientFrame{Type: protocol.TypeSend, CT: ct})
	if err := c.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatal(err)
	}
	e := bw.waitFor("forged message", func(e chat.Event) bool { return e.Kind == chat.KindMeerkat || e.Text == "forged" })
	if e.Kind != chat.KindMeerkat {
		t.Fatalf("forged signature accepted: %+v", e)
	}
}

func TestLongestMessagesFit(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice := newManager(t, "alice")
	aw := watch(t, alice)
	if _, err := alice.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"x", "<", `"`, "미"} {
		text := strings.Repeat(c, protocol.MaxText/len(c))
		if err := alice.Send(ctx, "", chat.KindMessage, text); err != nil {
			t.Fatalf("%q x %d: %v", c, len(text), err)
		}
		aw.waitFor("echo", func(e chat.Event) bool { return e.Text == text })
	}
}

func TestPendingThenConfirmed(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice := newManager(t, "alice")
	aw := watch(t, alice)
	if _, err := alice.Join(ctx, r.room("lobby"), "pw"); err != nil {
		t.Fatal(err)
	}
	if err := alice.SendAndWait(ctx, "", chat.KindMessage, "hello", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	p := aw.waitFor("pending", func(e chat.Event) bool { return e.Text == "hello" })
	if !p.Pending || !p.Self || p.ID == "" {
		t.Fatalf("first copy should be pending: %+v", p)
	}
	c := aw.waitFor("confirmed", func(e chat.Event) bool { return e.Text == "hello" })
	if c.Pending || c.ID != p.ID || c.Seq == 0 {
		t.Fatalf("second copy should be the confirmed one: %+v", c)
	}
	// A late subscriber sees only the confirmed copy.
	ch, cancel := alice.Subscribe(100)
	defer cancel()
	n := 0
	for e := range ch {
		if e.Kind == chat.KindSync {
			break
		}
		if e.Text == "hello" {
			n++
			if e.Pending {
				t.Fatal("pending copy left in scrollback")
			}
		}
	}
	if n != 1 {
		t.Fatalf("scrollback has %d copies", n)
	}
}

func TestSendAndWaitWhileRelayDown(t *testing.T) {
	r := startRelay(t)
	ctx := context.Background()
	alice, bob := newManager(t, "alice"), newManager(t, "bob")
	aw, bw := watch(t, alice), watch(t, bob)
	_, _ = alice.Join(ctx, r.room("lobby"), "pw")
	_, _ = bob.Join(ctx, r.room("lobby"), "pw")
	addr := r.addr
	r.stop()
	aw.waitFor("offline", func(e chat.Event) bool { return e.Kind == chat.KindStatus && e.Status == chat.StatusOffline })
	err := alice.SendAndWait(ctx, "", chat.KindMessage, "eventually", 200*time.Millisecond)
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("got %v", err)
	}
	r.start(addr)
	bw.waitFor("delivered after all", func(e chat.Event) bool { return e.Text == "eventually" && !e.Pending })
}
