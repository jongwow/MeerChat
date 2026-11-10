package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/identity"
	"github.com/jongwow/meerchat/internal/protocol"
)

// Options configure a Manager.
type Options struct {
	Nick   string
	Params crypt.Params
	Logger *slog.Logger
	// Scrollback is how many events are kept per room for late subscribers.
	Scrollback int
	// AllowAddress, if set, vetoes joins. The SSH demo uses it to keep
	// visitors on the local relay.
	AllowAddress func(chat.Address) error
	// Identity signs outgoing messages. Default: a fresh one per Manager.
	Identity *identity.Identity
	// Book remembers which key each nickname used. Default: in memory.
	Book *identity.Book
}

// Manager is a set of joined rooms plus an event bus. The daemon and the SSH
// demo both drive one; the TUI and CLI talk to it through the Backend
// interface.
type Manager struct {
	opts Options
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	kdf    chan struct{} // bounds concurrent Argon2 runs

	mu      sync.Mutex
	nick    string
	rooms   map[string]*joined
	order   []string
	current string
	subs    map[*sub]struct{}
}

type joined struct {
	conn   *roomConn
	cancel context.CancelFunc
	done   chan struct{}
	scroll []chat.Event
}

type sub struct {
	ch chan chat.Event
}

// Errors returned by Manager.
var (
	ErrNoRoom        = errors.New("not in any room; join one with: meer join meer://host/room")
	ErrAlreadyJoined = errors.New("already in that room")
)

// NewManager returns a Manager. Call Close when done.
func NewManager(opts Options) (*Manager, error) {
	if opts.Nick == "" {
		opts.Nick = "meer"
	}
	if err := protocol.ValidateNick(opts.Nick); err != nil {
		return nil, err
	}
	if opts.Params == (crypt.Params{}) {
		opts.Params = crypt.DefaultParams
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Scrollback == 0 {
		opts.Scrollback = 500
	}
	if opts.Identity == nil {
		opts.Identity = identity.Generate()
	}
	if opts.Book == nil {
		opts.Book = identity.NewBook()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts:   opts,
		log:    opts.Logger,
		ctx:    ctx,
		cancel: cancel,
		kdf:    make(chan struct{}, 2),
		nick:   opts.Nick,
		rooms:  make(map[string]*joined),
		subs:   make(map[*sub]struct{}),
	}, nil
}

// Close leaves every room and closes all subscriptions.
func (m *Manager) Close() {
	m.mu.Lock()
	var rooms []*joined
	for _, j := range m.rooms {
		rooms = append(rooms, j)
	}
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, j := range rooms {
		_, _ = j.conn.say(ctx, protocol.KindLeave, "")
	}
	m.cancel()
	for _, j := range rooms {
		<-j.done
	}
	m.mu.Lock()
	for s := range m.subs {
		close(s.ch)
		delete(m.subs, s)
	}
	m.mu.Unlock()
}

// Nick returns the current nickname.
func (m *Manager) Nick() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nick
}

// SetNick changes the nickname and announces it in every room.
func (m *Manager) SetNick(ctx context.Context, nick string) error {
	if err := protocol.ValidateNick(nick); err != nil {
		return err
	}
	m.mu.Lock()
	old := m.nick
	m.nick = nick
	conns := m.connsLocked()
	m.mu.Unlock()
	if old == nick {
		return nil
	}
	for _, c := range conns {
		_, _ = c.say(ctx, protocol.KindNick, old) // queued for rooms that are offline
	}
	return nil
}

func (m *Manager) connsLocked() []*roomConn {
	out := make([]*roomConn, 0, len(m.order))
	for _, u := range m.order {
		out = append(out, m.rooms[u].conn)
	}
	return out
}

// Join derives the room key, connects, and starts keeping the room alive.
// It returns once the first connection attempt succeeds or fails; on failure
// the room is not joined.
func (m *Manager) Join(ctx context.Context, address, password string) (chat.RoomInfo, error) {
	addr, err := chat.ParseAddress(address)
	if err != nil {
		return chat.RoomInfo{}, err
	}
	if m.opts.AllowAddress != nil {
		if err := m.opts.AllowAddress(addr); err != nil {
			return chat.RoomInfo{}, err
		}
	}
	url := addr.String()
	m.mu.Lock()
	_, exists := m.rooms[url]
	m.mu.Unlock()
	if exists {
		return chat.RoomInfo{}, ErrAlreadyJoined
	}

	select {
	case m.kdf <- struct{}{}:
	case <-ctx.Done():
		return chat.RoomInfo{}, ctx.Err()
	}
	key, err := crypt.DeriveKey(addr.Room, password, m.opts.Params)
	<-m.kdf
	if err != nil {
		return chat.RoomInfo{}, err
	}

	m.mu.Lock()
	if _, exists := m.rooms[url]; exists {
		m.mu.Unlock()
		return chat.RoomInfo{}, ErrAlreadyJoined
	}
	rctx, cancel := context.WithCancel(m.ctx)
	j := &joined{cancel: cancel, done: make(chan struct{})}
	j.conn = newRoomConn(addr, key, m.opts.Identity, m.opts.Book, m.Nick, m.emit, m.log)
	m.rooms[url] = j
	m.order = append(m.order, url)
	m.mu.Unlock()

	go func() {
		defer close(j.done)
		j.conn.run(rctx)
	}()

	select {
	case <-j.conn.first:
	case <-ctx.Done():
		m.remove(url)
		return chat.RoomInfo{}, ctx.Err()
	}
	if err := j.conn.firstResult; err != nil {
		m.remove(url)
		return chat.RoomInfo{}, err
	}

	m.mu.Lock()
	m.current = url
	m.mu.Unlock()
	info := j.conn.info()
	m.emit(chat.Event{Room: url, Kind: chat.KindSystem, Time: time.Now(),
		Text: fmt.Sprintf("joined %s · key fingerprint %s", url, info.Fingerprint)})
	return info, nil
}

func (m *Manager) remove(url string) *joined {
	m.mu.Lock()
	j, ok := m.rooms[url]
	if ok {
		delete(m.rooms, url)
		for i, u := range m.order {
			if u == url {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
		if m.current == url {
			m.current = ""
			if n := len(m.order); n > 0 {
				m.current = m.order[n-1]
			}
		}
	}
	m.mu.Unlock()
	if ok {
		j.cancel()
		<-j.done
	}
	return j
}

// Leave says goodbye and disconnects from a room.
func (m *Manager) Leave(ctx context.Context, room string) error {
	url, err := m.Resolve(room)
	if err != nil {
		return err
	}
	m.mu.Lock()
	j, ok := m.rooms[url]
	m.mu.Unlock()
	if !ok {
		return ErrNoRoom
	}
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, _ = j.conn.say(sctx, protocol.KindLeave, "")
	cancel()
	m.remove(url)
	m.emit(chat.Event{Room: url, Kind: chat.KindSystem, Time: time.Now(), Text: "left " + url})
	return nil
}

// ErrNotConfirmed means a message is queued but the relay has not echoed it
// within the wait time. It stays queued and is retried on reconnect.
var ErrNotConfirmed = errors.New("not confirmed by the relay yet; still queued and will be retried while the daemon runs")

// Send posts a message (kind "message") or an action (kind "action") and
// returns once it is queued.
func (m *Manager) Send(ctx context.Context, room, kind, text string) error {
	_, err := m.send(ctx, room, kind, text)
	return err
}

// SendAndWait is Send, then waits up to wait for the relay to echo the
// message back, which means it was stored and fanned out.
func (m *Manager) SendAndWait(ctx context.Context, room, kind, text string, wait time.Duration) error {
	done, err := m.send(ctx, room, kind, text)
	if err != nil || wait <= 0 {
		return err
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-done:
		return nil
	case <-t.C:
		return ErrNotConfirmed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) send(ctx context.Context, room, kind, text string) (<-chan struct{}, error) {
	url, err := m.Resolve(room)
	if err != nil {
		return nil, err
	}
	pk := protocol.KindMessage
	if kind == chat.KindAction {
		pk = protocol.KindAction
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("empty message")
	}
	if len(text) > protocol.MaxText {
		return nil, fmt.Errorf("message longer than %d bytes", protocol.MaxText)
	}
	m.mu.Lock()
	j, ok := m.rooms[url]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNoRoom
	}
	return j.conn.say(ctx, pk, text)
}

// Identity returns this device's key fingerprint.
func (m *Manager) Identity() string { return m.opts.Identity.Fingerprint() }

// Trust accepts the key nick most recently used in room.
func (m *Manager) Trust(room, nick string) (string, error) {
	url, err := m.Resolve(room)
	if err != nil {
		return "", err
	}
	pub, err := m.opts.Book.Accept(url, nick)
	if err != nil {
		return "", err
	}
	fp := identity.Fingerprint(pub)
	m.emit(chat.Event{Room: url, Kind: chat.KindSystem, Time: time.Now(),
		Text: fmt.Sprintf("now trusting %s's key %s", nick, fp)})
	return fp, nil
}

// Rooms lists joined rooms in join order.
func (m *Manager) Rooms() []chat.RoomInfo {
	m.mu.Lock()
	conns := m.connsLocked()
	m.mu.Unlock()
	out := make([]chat.RoomInfo, len(conns))
	for i, c := range conns {
		out[i] = c.info()
	}
	return out
}

// Resolve maps a user-supplied room reference to a joined room URL. The
// empty string means the most recently joined room. Otherwise it accepts a
// full address or a room name that is unique among joined rooms.
func (m *Manager) Resolve(ref string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.order) == 0 {
		return "", ErrNoRoom
	}
	if ref == "" {
		return m.current, nil
	}
	if _, ok := m.rooms[ref]; ok {
		return ref, nil
	}
	if a, err := chat.ParseAddress(ref); err == nil && strings.Contains(ref, "/") {
		if _, ok := m.rooms[a.String()]; ok {
			return a.String(), nil
		}
	}
	var match []string
	for _, u := range m.order {
		if m.rooms[u].conn.addr.Room == ref {
			match = append(match, u)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("not in a room called %q", ref)
	default:
		return "", fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(match, ", "))
	}
}

// Subscribe streams events. The channel first receives up to backlog recent
// events per room, then live events. If the subscriber falls too far behind
// the channel is closed; resubscribe to resynchronize. Call cancel when done.
func (m *Manager) Subscribe(backlog int) (<-chan chat.Event, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pre []chat.Event
	for _, u := range m.order {
		sc := m.rooms[u].scroll
		pre = append(pre, sc[max(0, len(sc)-backlog):]...)
	}
	s := &sub{ch: make(chan chat.Event, len(pre)+1025)}
	for _, e := range pre {
		s.ch <- e
	}
	s.ch <- chat.Event{Kind: chat.KindSync, Time: time.Now()}
	m.subs[s] = struct{}{}
	return s.ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subs[s]; ok {
			delete(m.subs, s)
			close(s.ch)
		}
	}
}

func (m *Manager) emit(e chat.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.rooms[e.Room]; ok && e.Kind != chat.KindPeers {
		if e.ID != "" && !e.Pending {
			// The confirmed (or failed) copy replaces the pending one.
			for i := range j.scroll {
				if j.scroll[i].ID == e.ID && j.scroll[i].Pending {
					j.scroll = append(j.scroll[:i], j.scroll[i+1:]...)
					break
				}
			}
		}
		j.scroll = append(j.scroll, e)
		if over := len(j.scroll) - m.opts.Scrollback; over > 0 {
			j.scroll = append(j.scroll[:0:0], j.scroll[over:]...)
		}
	}
	for s := range m.subs {
		select {
		case s.ch <- e:
		default:
			m.log.Warn("subscriber too slow, dropping")
			delete(m.subs, s)
			close(s.ch)
		}
	}
}
