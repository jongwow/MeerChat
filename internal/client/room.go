// Package client connects to MeerChat relays: one WebSocket per room, with
// end-to-end encryption, reconnects and de-duplication.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jongwow/meerchat/internal/chat"
	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/identity"
	"github.com/jongwow/meerchat/internal/meerkat"
	"github.com/jongwow/meerchat/internal/protocol"
)

// ErrOffline is returned when a message cannot even be queued.
var ErrOffline = errors.New("room is offline and the outbox is full")

const (
	dialTimeout  = 10 * time.Second
	writeTimeout = 10 * time.Second
	pingInterval = 30 * time.Second
	minBackoff   = 500 * time.Millisecond
	maxBackoff   = 30 * time.Second

	// Messages wait in the outbox until the relay echoes them back. After a
	// reconnect, whatever is still there is sent again; receivers drop
	// duplicates by payload ID. That makes delivery at-least-once on the
	// wire and exactly-once on screen.
	maxOutbox = 256
	outboxTTL = 10 * time.Minute
)

type pending struct {
	id    string
	kind  string // chat event kind, for the failure notice
	text  string
	frame []byte
	at    time.Time
	done  chan struct{} // closed when the relay echoes it
}

// roomConn owns the connection to one room and survives reconnects.
type roomConn struct {
	addr chat.Address
	url  string
	key  *crypt.Key
	id   *identity.Identity
	book *identity.Book
	nick func() string
	emit func(chat.Event)
	log  *slog.Logger

	// first is closed once the first connection attempt has either reached
	// the relay or failed; firstResult holds the outcome.
	first       chan struct{}
	firstResult error
	firstOnce   sync.Once

	mu        sync.Mutex
	conn      *websocket.Conn
	status    string
	peers     int
	lastSeq   uint64
	selfPeers map[string]bool
	seen      *dedupe
	announced bool
	outbox    []pending
}

func newRoomConn(addr chat.Address, key *crypt.Key, id *identity.Identity, book *identity.Book, nick func() string, emit func(chat.Event), log *slog.Logger) *roomConn {
	return &roomConn{
		addr:      addr,
		url:       addr.String(),
		key:       key,
		id:        id,
		book:      book,
		nick:      nick,
		emit:      emit,
		log:       log.With("room", addr.String()),
		first:     make(chan struct{}),
		status:    chat.StatusConnecting,
		selfPeers: make(map[string]bool),
		seen:      newDedupe(4096),
	}
}

func (r *roomConn) info() chat.RoomInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return chat.RoomInfo{
		URL:         r.url,
		Name:        r.addr.Room,
		Server:      r.addr.Host,
		Fingerprint: r.key.Fingerprint(),
		Status:      r.status,
		Peers:       r.peers,
	}
}

// run keeps the room connected until ctx is cancelled.
func (r *roomConn) run(ctx context.Context) {
	backoff := minBackoff
	for attempt := 0; ; attempt++ {
		started := time.Now()
		err := r.session(ctx, attempt == 0)
		if ctx.Err() != nil {
			r.setStatus(chat.StatusOffline, "")
			return
		}
		r.setStatus(chat.StatusOffline, err.Error())
		if time.Since(started) > maxBackoff {
			backoff = minBackoff
		}
		// Full jitter keeps a relay restart from causing a thundering herd.
		wait := time.Duration(rand.Int64N(int64(backoff))) + minBackoff //nolint:gosec // jitter, not secrets
		r.log.Debug("reconnecting", "in", wait, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (r *roomConn) setStatus(status, detail string) {
	r.mu.Lock()
	changed := r.status != status
	r.status = status
	r.mu.Unlock()
	if changed {
		r.emit(chat.Event{Room: r.url, Kind: chat.KindStatus, Time: time.Now(), Status: status, Text: detail})
	}
}

// session runs one connection until it fails.
func (r *roomConn) session(ctx context.Context, first bool) (err error) {
	defer func() {
		if first {
			r.settleFirst(err)
		}
	}()
	r.setStatus(chat.StatusConnecting, "")

	r.mu.Lock()
	since := r.lastSeq
	r.mu.Unlock()

	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	c, _, err := websocket.Dial(dctx, fmt.Sprintf("%s?since=%d", r.addr.WebSocketURL(r.key.Route()), since),
		&websocket.DialOptions{Subprotocols: []string{protocol.Subprotocol}})
	if err != nil {
		return fmt.Errorf("connect %s: %w", r.addr.Host, err)
	}
	defer c.CloseNow()
	c.SetReadLimit(protocol.MaxFrame)

	welcome, err := readFrame(dctx, c)
	if err != nil {
		return err
	}
	if welcome.Type != protocol.TypeWelcome {
		return fmt.Errorf("unexpected first frame %q", welcome.Type)
	}

	r.mu.Lock()
	if welcome.Head < r.lastSeq {
		// The relay lost history (restarted without a database). Sequence
		// numbers start over and it replays from the beginning; payload IDs
		// catch anything we have already shown.
		r.lastSeq = 0
	}
	historyEnd := welcome.Head
	r.selfPeers[welcome.You] = true
	r.peers = welcome.Peers
	r.conn = c
	announce := !r.announced
	r.announced = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.conn = nil
		r.mu.Unlock()
	}()

	r.setStatus(chat.StatusOnline, "")
	r.emit(chat.Event{Room: r.url, Kind: chat.KindPeers, Time: time.Now(), Peers: welcome.Peers})
	if announce {
		if _, err := r.say(ctx, protocol.KindJoin, ""); err != nil {
			return err
		}
	}
	if err := r.flushOutbox(ctx, c); err != nil {
		return err
	}
	r.settleFirst(nil)

	pctx, stopPing := context.WithCancel(ctx)
	defer stopPing()
	go r.keepalive(pctx, c)

	for {
		f, err := readFrame(ctx, c)
		if err != nil {
			if s := websocket.CloseStatus(err); s != -1 {
				return fmt.Errorf("relay closed connection: %v", s)
			}
			return err
		}
		switch f.Type {
		case protocol.TypeMessage:
			r.handleMessage(f, f.Seq <= historyEnd)
		case protocol.TypePresence:
			r.mu.Lock()
			r.peers = f.Peers
			r.mu.Unlock()
			r.emit(chat.Event{Room: r.url, Kind: chat.KindPeers, Time: time.Now(), Peers: f.Peers, Peer: f.Peer})
		case protocol.TypeError:
			r.emit(chat.Event{Room: r.url, Kind: chat.KindSystem, Time: time.Now(), Text: "relay: " + f.Message})
		}
	}
}

func (r *roomConn) settleFirst(err error) {
	r.firstOnce.Do(func() {
		r.firstResult = err
		close(r.first)
	})
}

func (r *roomConn) keepalive(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				_ = c.CloseNow()
				return
			}
		}
	}
}

func readFrame(ctx context.Context, c *websocket.Conn) (*protocol.ServerFrame, error) {
	_, b, err := c.Read(ctx)
	if err != nil {
		return nil, err
	}
	var f protocol.ServerFrame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("bad frame from relay: %w", err)
	}
	return &f, nil
}

func (r *roomConn) handleMessage(f *protocol.ServerFrame, history bool) {
	r.mu.Lock()
	if f.Seq != 0 && f.Seq <= r.lastSeq {
		r.mu.Unlock()
		return
	}
	r.lastSeq = max(r.lastSeq, f.Seq)
	self := r.selfPeers[f.Peer]
	r.mu.Unlock()

	ev := chat.Event{
		Room:    r.url,
		Time:    time.UnixMilli(f.TS),
		Seq:     f.Seq,
		Peer:    f.Peer,
		Self:    self,
		History: history,
	}
	p, pub, ok := r.open(f.CT)
	if !ok {
		ev.Kind = chat.KindMeerkat
		ev.Nick = meerkat.Name(f.Peer)
		ev.Text = meerkat.Speak(f.CT)
		r.emit(ev)
		return
	}
	// The device key is a better notion of "me" than the per-connection
	// peer ID: it survives reconnects and covers messages replayed from
	// before this session.
	if bytes.Equal(pub, r.id.Public()) {
		self = true
		ev.Self = true
	}
	if self {
		r.acked(p.ID)
	}
	if !r.seen.add(p.ID) {
		return // replayed or duplicated
	}
	ev.Nick, ev.Text, ev.ID = p.Nick, p.Text, p.ID
	ev.Key = identity.Fingerprint(pub)
	switch {
	case self:
		ev.Trust = identity.TrustSelf
	case p.Kind == protocol.KindNick:
		ev.Trust = r.book.Rename(r.url, p.Text, p.Nick, pub)
	default:
		ev.Trust = r.book.Observe(r.url, p.Nick, pub)
	}
	switch p.Kind {
	case protocol.KindMessage:
		ev.Kind = chat.KindMessage
	case protocol.KindAction:
		ev.Kind = chat.KindAction
	case protocol.KindJoin:
		ev.Kind = chat.KindJoin
	case protocol.KindLeave:
		ev.Kind = chat.KindLeave
	case protocol.KindNick:
		ev.Kind = chat.KindNick
	}
	r.emit(ev)
}

// open decrypts a ciphertext, checks the sender's signature, and validates
// the payload. Anything that fails, for any reason, is noise.
func (r *roomConn) open(ct []byte) (*protocol.Payload, []byte, bool) {
	pt, err := r.key.Open(ct)
	if err != nil {
		return nil, nil, false
	}
	var env protocol.Envelope
	if err := json.Unmarshal(pt, &env); err != nil {
		return nil, nil, false
	}
	if !identity.Verify(env.Key, protocol.SigningInput(r.key.Route(), env.Body), env.Sig) {
		return nil, nil, false
	}
	var p protocol.Payload
	if err := json.Unmarshal(env.Body, &p); err != nil || p.Validate() != nil {
		return nil, nil, false
	}
	return &p, env.Key, true
}

// seal signs a payload and encrypts it.
func (r *roomConn) seal(p protocol.Payload) ([]byte, error) {
	// No HTML escaping: it can blow "<<<" up sixfold for no benefit here.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return nil, err
	}
	body := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	pt, err := json.Marshal(protocol.Envelope{
		Body: body,
		Key:  r.id.Public(),
		Sig:  r.id.Sign(protocol.SigningInput(r.key.Route(), body)),
	})
	if err != nil {
		return nil, err
	}
	return r.key.Seal(pt)
}

// say encrypts and sends a payload. Chat messages, actions and nick changes
// go through the outbox and survive disconnects; join and leave
// announcements are only meaningful right now, so they do not.
//
// For durable payloads it returns a channel that is closed once the relay
// has echoed the message back; chat messages and actions also appear
// immediately as pending events.
func (r *roomConn) say(ctx context.Context, kind, text string) (<-chan struct{}, error) {
	p := protocol.NewPayload(kind, r.nick(), text)
	if err := p.Validate(); err != nil {
		return nil, err
	}
	ct, err := r.seal(p)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(protocol.ClientFrame{Type: protocol.TypeSend, CT: ct})
	if err != nil {
		return nil, err
	}
	durable := kind != protocol.KindJoin && kind != protocol.KindLeave
	evKind := map[string]string{protocol.KindMessage: chat.KindMessage, protocol.KindAction: chat.KindAction}[kind]

	var done chan struct{}
	r.mu.Lock()
	c := r.conn
	if durable {
		if len(r.outbox) >= maxOutbox {
			r.mu.Unlock()
			return nil, ErrOffline
		}
		done = make(chan struct{})
		r.outbox = append(r.outbox, pending{id: p.ID, kind: evKind, text: text, frame: b, at: time.Now(), done: done})
	}
	r.mu.Unlock()

	if evKind != "" {
		r.emit(chat.Event{
			Room: r.url, Kind: evKind, Time: time.Now(), Nick: p.Nick, Text: text, Self: true,
			Key: r.id.Fingerprint(), Trust: identity.TrustSelf, ID: p.ID, Pending: true,
		})
	}

	if c == nil {
		if durable {
			return done, nil // queued; sent on reconnect
		}
		return nil, ErrOffline
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := c.Write(wctx, websocket.MessageText, b); err != nil && !durable {
		return nil, err
	}
	return done, nil
}

// acked removes a message from the outbox once the relay has echoed it.
func (r *roomConn) acked(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, p := range r.outbox {
		if p.id == id {
			r.outbox = append(r.outbox[:i], r.outbox[i+1:]...)
			close(p.done)
			return
		}
	}
}

// flushOutbox resends everything not yet echoed, dropping stale entries.
func (r *roomConn) flushOutbox(ctx context.Context, c *websocket.Conn) error {
	r.mu.Lock()
	keep := r.outbox[:0]
	var expired []pending
	for _, p := range r.outbox {
		if time.Since(p.at) < outboxTTL {
			keep = append(keep, p)
		} else {
			expired = append(expired, p)
		}
	}
	r.outbox = keep
	frames := make([][]byte, len(keep))
	for i, p := range keep {
		frames[i] = p.frame
	}
	r.mu.Unlock()
	for _, p := range expired {
		if p.kind != "" {
			r.emit(chat.Event{Room: r.url, Kind: p.kind, Time: time.Now(), Nick: r.nick(), Text: p.text,
				Self: true, Trust: identity.TrustSelf, ID: p.id, Failed: true})
		}
	}
	for _, b := range frames {
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := c.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// dedupe remembers the last n IDs.
type dedupe struct {
	set  map[string]struct{}
	ring []string
	next int
}

func newDedupe(n int) *dedupe {
	return &dedupe{set: make(map[string]struct{}, n), ring: make([]string, n)}
}

// add records id and reports whether it was new.
func (d *dedupe) add(id string) bool {
	if _, ok := d.set[id]; ok {
		return false
	}
	if old := d.ring[d.next]; old != "" {
		delete(d.set, old)
	}
	d.ring[d.next] = id
	d.set[id] = struct{}{}
	d.next = (d.next + 1) % len(d.ring)
	return true
}
