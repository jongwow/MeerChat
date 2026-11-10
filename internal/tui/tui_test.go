package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jongwow/meerchat/internal/chat"
)

type fakeBackend struct {
	mu      sync.Mutex
	rooms   []chat.RoomInfo
	sent    []string
	joined  []string
	trusted []string
	nick    string
	ch      chan chat.Event
}

func newFake() *fakeBackend {
	return &fakeBackend{nick: "alice", ch: make(chan chat.Event, 100)}
}

func (f *fakeBackend) Join(_ context.Context, address, password string) (chat.RoomInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if password == "fail" {
		return chat.RoomInfo{}, errors.New("nope")
	}
	a, err := chat.ParseAddress(address)
	if err != nil {
		return chat.RoomInfo{}, err
	}
	info := chat.RoomInfo{URL: a.String(), Name: a.Room, Server: a.Host, Fingerprint: "ABCD-EF01", Status: chat.StatusOnline, Peers: 1}
	f.rooms = append(f.rooms, info)
	f.joined = append(f.joined, address+"|"+password)
	return info, nil
}
func (f *fakeBackend) Leave(context.Context, string) error { return nil }
func (f *fakeBackend) Send(_ context.Context, room, kind, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, kind+":"+text)
	return nil
}
func (f *fakeBackend) Rooms(context.Context) ([]chat.RoomInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chat.RoomInfo(nil), f.rooms...), nil
}
func (f *fakeBackend) Nick(context.Context) (string, error) { return f.nick, nil }
func (f *fakeBackend) SetNick(_ context.Context, n string) error {
	f.nick = n
	return nil
}
func (f *fakeBackend) Subscribe(context.Context, int) (<-chan chat.Event, error) { return f.ch, nil }
func (f *fakeBackend) Identity(context.Context) (string, error)                  { return "aaaa-bbbb-cccc-dddd", nil }
func (f *fakeBackend) Trust(_ context.Context, _, nick string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trusted = append(f.trusted, nick)
	return "eeee-ffff-0000-1111", nil
}

// drive feeds a message through Update and runs resulting commands
// synchronously, a few levels deep, ignoring ones that block.
func drive(m *Model, msg tea.Msg) {
	_, cmd := m.Update(msg)
	run(m, cmd, 0)
}

func run(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 3 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				run(m, c, depth+1)
			}
		case nil:
		default:
			_, next := m.Update(msg)
			run(m, next, depth+1)
		}
	case <-time.After(50 * time.Millisecond):
		// blocking command (event wait, tick); skip
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		drive(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func press(m *Model, code rune) { drive(m, tea.KeyPressMsg{Code: code}) }

func screen(m *Model) string { return ansi.Strip(m.View().Content) }

func setup(t *testing.T) (*Model, *fakeBackend) {
	be := newFake()
	m := New(context.Background(), be, Options{})
	drive(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	run(m, m.Init(), 0)
	return m, be
}

func TestWelcomeScreen(t *testing.T) {
	m, _ := setup(t)
	s := screen(m)
	for _, want := range []string{"welcome to meer", "/join meer://host/room", "meerkats"} {
		if !strings.Contains(s, want) {
			t.Errorf("welcome screen missing %q:\n%s", want, s)
		}
	}
}

func TestJoinFlowAndSend(t *testing.T) {
	m, be := setup(t)
	typeText(m, "/join meer://example.com/lobby")
	press(m, tea.KeyEnter)
	if !strings.Contains(screen(m), "password ›") {
		t.Fatalf("expected password prompt:\n%s", screen(m))
	}
	typeText(m, "s3cret")
	if strings.Contains(screen(m), "s3cret") {
		t.Fatal("password echoed on screen")
	}
	press(m, tea.KeyEnter)
	if len(be.joined) != 1 || be.joined[0] != "meer://example.com/lobby|s3cret" {
		t.Fatalf("joined %v", be.joined)
	}
	s := screen(m)
	if !strings.Contains(s, "#lobby") || !strings.Contains(s, "ABCD-EF01") {
		t.Fatalf("room not shown:\n%s", s)
	}

	typeText(m, "hello world")
	press(m, tea.KeyEnter)
	typeText(m, "/me waves")
	press(m, tea.KeyEnter)
	typeText(m, "//not a command")
	press(m, tea.KeyEnter)
	want := []string{"message:hello world", "action:waves", "message:/not a command"}
	if strings.Join(be.sent, ",") != strings.Join(want, ",") {
		t.Fatalf("sent %v", be.sent)
	}
}

func TestEscCancelsPasswordPrompt(t *testing.T) {
	m, be := setup(t)
	typeText(m, "/join meer://example.com/lobby")
	press(m, tea.KeyEnter)
	press(m, tea.KeyEscape)
	if strings.Contains(screen(m), "password ›") || len(be.joined) != 0 {
		t.Fatal("esc did not cancel the join")
	}
}

func TestEventsRenderAndAreSanitized(t *testing.T) {
	m, be := setup(t)
	info, _ := be.Join(context.Background(), "meer://h/lobby", "pw")
	drive(m, roomsMsg([]chat.RoomInfo{info}))
	now := time.Now()
	for _, e := range []chat.Event{
		{Room: info.URL, Kind: chat.KindMessage, Nick: "bob", Text: "hi \x1b[2J\x1b]0;pwned\x07there", Time: now},
		{Room: info.URL, Kind: chat.KindMeerkat, Nick: "meerkat-ab12", Text: "meer mrr", Time: now},
		{Room: info.URL, Kind: chat.KindJoin, Nick: "carol", Time: now},
		{Room: info.URL, Kind: chat.KindNick, Nick: "robert", Text: "bob", Time: now},
	} {
		drive(m, eventMsg(e))
	}
	raw := m.View().Content
	if strings.Contains(raw, "\x1b]0;pwned") || strings.Contains(raw, "\x1b[2J") {
		t.Fatal("escape sequence from a message reached the terminal")
	}
	s := screen(m)
	for _, want := range []string{"bob", "hi [2J]0;pwnedthere", "🐾 meerkat-ab12", "meer mrr", "carol joined", "bob is now known as robert"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q:\n%s", want, s)
		}
	}
}

func TestUnreadAndMentions(t *testing.T) {
	m, be := setup(t)
	a, _ := be.Join(context.Background(), "meer://h/one", "pw")
	b, _ := be.Join(context.Background(), "meer://h/two", "pw")
	drive(m, roomsMsg([]chat.RoomInfo{a, b}))
	drive(m, nickMsg("alice"))
	m.setFocus(0)

	drive(m, eventMsg(chat.Event{Room: b.URL, Kind: chat.KindMessage, Nick: "bob", Text: "yo", Time: time.Now()}))
	drive(m, eventMsg(chat.Event{Room: b.URL, Kind: chat.KindMessage, Nick: "bob", Text: "hey alice!", Time: time.Now()}))
	r := m.room(b.URL)
	if r.unread != 2 || !r.mention {
		t.Fatalf("unread=%d mention=%v", r.unread, r.mention)
	}
	if !strings.Contains(screen(m), "#two @") {
		t.Fatalf("mention badge missing:\n%s", screen(m))
	}
	press(m, tea.KeyTab)
	if m.focused() != r || r.unread != 0 || r.mention {
		t.Fatal("switching rooms should clear unread")
	}
}

func TestMentionMatching(t *testing.T) {
	m := &Model{nick: "al"}
	for text, want := range map[string]bool{
		"hi al": true, "@al look": true, "AL!": true, "also": false, "pal": false,
	} {
		if got := m.mentions(chat.Event{Kind: chat.KindMessage, Text: text}); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	m, _ := setup(t)
	typeText(m, "/bogus")
	press(m, tea.KeyEnter)
	if !strings.Contains(screen(m), "unknown command /bogus") {
		t.Fatalf("no error shown:\n%s", screen(m))
	}
}

func TestNarrowTerminal(t *testing.T) {
	m, be := setup(t)
	info, _ := be.Join(context.Background(), "meer://h/lobby", "pw")
	drive(m, roomsMsg([]chat.RoomInfo{info}))
	drive(m, tea.WindowSizeMsg{Width: 30, Height: 10})
	drive(m, eventMsg(chat.Event{Room: info.URL, Kind: chat.KindMessage, Nick: "a-very-long-nickname", Text: strings.Repeat("word ", 40), Time: time.Now()}))
	for i, line := range strings.Split(screen(m), "\n") {
		if w := ansi.StringWidth(line); w > 30 {
			t.Errorf("line %d is %d wide: %q", i, w, line)
		}
	}
}

func TestReconnectNotices(t *testing.T) {
	m, be := setup(t)
	info, _ := be.Join(context.Background(), "meer://h/lobby", "pw")
	drive(m, roomsMsg([]chat.RoomInfo{info}))
	st := func(s string) {
		drive(m, eventMsg(chat.Event{Room: info.URL, Kind: chat.KindStatus, Status: s, Time: time.Now()}))
	}
	st(chat.StatusConnecting)
	st(chat.StatusOnline) // initial connect, e.g. replayed from backlog
	if strings.Contains(screen(m), "reconnected") {
		t.Fatal("initial connect shown as reconnect")
	}
	st(chat.StatusOffline)
	st(chat.StatusConnecting)
	st(chat.StatusOffline) // repeated failure: one notice is enough
	st(chat.StatusOnline)
	s := screen(m)
	if strings.Count(s, "connection lost") != 1 || !strings.Contains(s, "reconnected") {
		t.Fatalf("got:\n%s", s)
	}
}

func TestKeyChangeWarning(t *testing.T) {
	m, be := setup(t)
	info, _ := be.Join(context.Background(), "meer://h/lobby", "pw")
	drive(m, roomsMsg([]chat.RoomInfo{info}))
	ev := func(text, key, trust string) {
		drive(m, eventMsg(chat.Event{Room: info.URL, Kind: chat.KindMessage, Nick: "alice", Text: text, Key: key, Trust: trust, Time: time.Now()}))
	}
	ev("hi", "1111-1111-1111-1111", "new")
	ev("it's me", "9999-9999-9999-9999", "changed")
	ev("really", "9999-9999-9999-9999", "changed")
	s := screen(m)
	if !strings.Contains(s, "⚠ alice") {
		t.Fatalf("no warning marker:\n%s", s)
	}
	if n := strings.Count(s, "speaking with a different"); n != 1 {
		t.Fatalf("explanation shown %d times:\n%s", n, s)
	}

	typeText(m, "/whois alice")
	press(m, tea.KeyEnter)
	if !strings.Contains(screen(m), "DIFFERENT key") {
		t.Fatalf("whois:\n%s", screen(m))
	}
	typeText(m, "/trust alice")
	press(m, tea.KeyEnter)
	if len(be.trusted) != 1 || be.trusted[0] != "alice" || !strings.Contains(screen(m), "now trusting alice") {
		t.Fatalf("trust: %v\n%s", be.trusted, screen(m))
	}
	typeText(m, "/whois")
	press(m, tea.KeyEnter)
	if !strings.Contains(screen(m), "aaaa-bbbb-cccc-dddd") {
		t.Fatalf("own identity:\n%s", screen(m))
	}
}
