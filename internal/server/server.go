// Package server is the MeerChat relay.
//
// The relay is deliberately dumb. It groups WebSocket connections by route ID,
// stamps each ciphertext with a sequence number, stores it, and fans it out.
// It cannot read messages, does not know room names, and has no notion of
// who is "allowed" in a room: possession of the key is the only membership.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/jongwow/meerchat/internal/crypt"
	"github.com/jongwow/meerchat/internal/protocol"
	"github.com/jongwow/meerchat/internal/store"
	"github.com/jongwow/meerchat/internal/version"
)

// Config tunes the relay. Zero values pick sensible defaults.
type Config struct {
	Store  store.Store
	Logger *slog.Logger

	// Replay is the most history sent to a (re)connecting client.
	Replay int
	// MaxPeersPerRoom caps connections per room.
	MaxPeersPerRoom int
	// MaxConns caps total connections.
	MaxConns int
	// Rate and Burst limit messages per connection.
	Rate  rate.Limit
	Burst int
	// SendBuffer is how many frames may queue for a peer before it is
	// considered too slow and disconnected.
	SendBuffer int
	// PingInterval is how often idle connections are pinged.
	PingInterval time.Duration
	// WriteTimeout bounds a single frame write.
	WriteTimeout time.Duration
	// Retention, if non-zero, prunes stored history older than this.
	Retention time.Duration
	// InsecureSkipOriginCheck disables the browser same-origin check.
	InsecureSkipOriginCheck bool
}

func (c *Config) defaults() {
	if c.Store == nil {
		c.Store = store.NewMemory(500)
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	if c.Replay == 0 {
		c.Replay = 200
	}
	if c.MaxPeersPerRoom == 0 {
		c.MaxPeersPerRoom = 512
	}
	if c.MaxConns == 0 {
		c.MaxConns = 10000
	}
	if c.Rate == 0 {
		c.Rate = 5
	}
	if c.Burst == 0 {
		c.Burst = 20
	}
	if c.SendBuffer == 0 {
		c.SendBuffer = max(c.Replay+64, 256)
	}
	if c.PingInterval == 0 {
		c.PingInterval = 25 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 10 * time.Second
	}
}

// Server is an http.Handler serving the MeerChat relay protocol.
type Server struct {
	cfg Config
	log *slog.Logger
	mux *http.ServeMux

	mu     sync.Mutex
	rooms  map[string]*room
	closed bool

	conns    atomic.Int64
	messages atomic.Uint64

	stop context.CancelFunc
	done chan struct{}
}

type room struct {
	route string
	mu    sync.Mutex // guards peers and orders Append+fan-out
	peers map[*peer]struct{}
}

type peer struct {
	id   string
	out  chan []byte
	conn *websocket.Conn
	once sync.Once
}

// kick closes the peer's connection with a status code. It does not block:
// a close handshake can take seconds and kick is called with locks held.
func (p *peer) kick(code websocket.StatusCode, reason string) {
	p.once.Do(func() {
		go func() { _ = p.conn.Close(code, reason) }()
	})
}

var errShutdown = errors.New("server shutting down")

// New returns a Server. Call Close to stop background work.
func New(cfg Config) *Server {
	cfg.defaults()
	ctx, stop := context.WithCancel(context.Background())
	s := &Server{
		cfg:   cfg,
		log:   cfg.Logger,
		rooms: make(map[string]*room),
		stop:  stop,
		done:  make(chan struct{}),
	}
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /v1/rooms/{route}/ws", s.handleWS)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET /v1/stats", s.handleStats)
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	go s.janitor(ctx)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// Close disconnects every peer and stops background work. Hijacked WebSocket
// connections are not tracked by http.Server.Shutdown, so call this too.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var all []*peer
	for _, r := range s.rooms {
		r.mu.Lock()
		for p := range r.peers {
			all = append(all, p)
		}
		r.mu.Unlock()
	}
	s.mu.Unlock()
	for _, p := range all {
		p.kick(websocket.StatusGoingAway, "server shutting down")
	}
	s.stop()
	<-s.done
}

// Stats is a snapshot of server load.
type Stats struct {
	Version  string `json:"version"`
	Rooms    int    `json:"rooms"`
	Conns    int64  `json:"connections"`
	Messages uint64 `json:"messages_relayed"`
}

// Stats returns current counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	n := len(s.rooms)
	s.mu.Unlock()
	return Stats{Version: version.Version(), Rooms: n, Conns: s.conns.Load(), Messages: s.messages.Load()}
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Stats())
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprintf(w, "meer relay %s\n\nThis server relays end-to-end encrypted ciphertext.\nIt cannot read your messages.\n\n    meer join meer://<this-host>/<room>\n\nhttps://github.com/jongwow/meerchat\n", version.Version())
}

func (s *Server) janitor(ctx context.Context) {
	defer close(s.done)
	if s.cfg.Retention <= 0 {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(min(s.cfg.Retention/4, 10*time.Minute))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.cfg.Store.Prune(ctx, time.Now().Add(-s.cfg.Retention))
			if err != nil {
				s.log.Error("prune", "err", err)
			} else if n > 0 {
				s.log.Info("pruned history", "messages", n)
			}
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	route := r.PathValue("route")
	if !crypt.ValidRouteID(route) {
		http.Error(w, "invalid room", http.StatusBadRequest)
		return
	}
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			http.Error(w, "invalid since", http.StatusBadRequest)
			return
		}
		since = n
	}
	if s.conns.Add(1) > int64(s.cfg.MaxConns) {
		s.conns.Add(-1)
		http.Error(w, "server full", http.StatusServiceUnavailable)
		return
	}
	defer s.conns.Add(-1)

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{protocol.Subprotocol},
		InsecureSkipVerify: s.cfg.InsecureSkipOriginCheck,
	})
	if err != nil {
		s.log.Debug("accept", "err", err)
		return
	}
	defer c.CloseNow()
	if c.Subprotocol() != protocol.Subprotocol {
		_ = c.Close(websocket.StatusPolicyViolation, "client must speak "+protocol.Subprotocol)
		return
	}
	c.SetReadLimit(protocol.MaxFrame)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	p := &peer{id: protocol.NewPeerID(), out: make(chan []byte, s.cfg.SendBuffer), conn: c}

	rm, err := s.join(ctx, route, p, since)
	if err != nil {
		_ = c.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	defer s.leave(rm, p)

	log := s.log.With("peer", p.id, "route", route[:8])
	log.Debug("connected")

	go s.writeLoop(ctx, c, p)
	err = s.readLoop(ctx, c, rm, p)
	if websocket.CloseStatus(err) == -1 {
		log.Debug("read", "err", err)
	}
	log.Debug("disconnected")
}

func (s *Server) readLoop(ctx context.Context, c *websocket.Conn, rm *room, p *peer) error {
	lim := rate.NewLimiter(s.cfg.Rate, s.cfg.Burst)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return err
		}
		if typ != websocket.MessageText {
			s.sendError(p, protocol.ErrBadFrame, "expected text frame")
			continue
		}
		f, err := protocol.DecodeClientFrame(b)
		if err != nil {
			s.sendError(p, protocol.ErrBadFrame, err.Error())
			continue
		}
		if !lim.Allow() {
			s.sendError(p, protocol.ErrRateLimited, "slow down")
			continue
		}
		if err := s.publish(ctx, rm, p, f.CT); err != nil {
			s.log.Error("publish", "err", err)
			s.sendError(p, protocol.ErrInternal, "could not store message")
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, c *websocket.Conn, p *peer) {
	ping := time.NewTicker(s.cfg.PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-p.out:
			wctx, cancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
			err := c.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				_ = c.CloseNow()
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, s.cfg.WriteTimeout)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				_ = c.CloseNow()
				return
			}
		}
	}
}

// join registers p in its room and queues the welcome frame and history.
// It holds the room lock across the history read so no message can slip in
// between the replay and live delivery.
//
// Lock order is always s.mu then room.mu.
func (s *Server) join(ctx context.Context, route string, p *peer, since uint64) (*room, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errShutdown
	}
	rm, ok := s.rooms[route]
	if !ok {
		rm = &room{route: route, peers: make(map[*peer]struct{})}
		s.rooms[route] = rm
	}
	rm.mu.Lock()
	s.mu.Unlock()

	err := s.admit(ctx, rm, p, since)
	rm.mu.Unlock()
	if err != nil {
		s.removeIfEmpty(rm)
		return nil, err
	}
	return rm, nil
}

// admit does the work of join. The caller holds rm.mu.
func (s *Server) admit(ctx context.Context, rm *room, p *peer, since uint64) error {
	if len(rm.peers) >= s.cfg.MaxPeersPerRoom {
		return errors.New("room full")
	}
	head, err := s.cfg.Store.Head(ctx, rm.route)
	if err != nil {
		return err
	}
	if since > head {
		// The client has seen sequence numbers we never issued: we lost
		// history (restart without a database). Replay what we have; the
		// client de-duplicates by payload ID.
		since = 0
	}
	var history []store.Message
	if since < head {
		history, err = s.cfg.Store.Since(ctx, rm.route, since, s.cfg.Replay)
		if err != nil {
			return err
		}
	}
	rm.peers[p] = struct{}{}

	// p.out has room for the welcome frame plus a full replay.
	p.out <- encode(protocol.ServerFrame{Type: protocol.TypeWelcome, You: p.id, Head: head, Peers: len(rm.peers)})
	for _, m := range history {
		p.out <- encode(protocol.ServerFrame{Type: protocol.TypeMessage, Seq: m.Seq, Peer: m.Peer, TS: m.TS, CT: m.CT})
	}
	s.fanout(rm, p, encode(protocol.ServerFrame{
		Type: protocol.TypePresence, Event: protocol.PresenceJoin, Peer: p.id, Peers: len(rm.peers),
	}))
	return nil
}

// removeIfEmpty drops rm from the room table if nobody is in it.
func (s *Server) removeIfEmpty(rm *room) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if len(rm.peers) == 0 && s.rooms[rm.route] == rm {
		delete(s.rooms, rm.route)
	}
}

func (s *Server) leave(rm *room, p *peer) {
	rm.mu.Lock()
	delete(rm.peers, p)
	s.fanout(rm, nil, encode(protocol.ServerFrame{
		Type: protocol.TypePresence, Event: protocol.PresenceLeave, Peer: p.id, Peers: len(rm.peers),
	}))
	rm.mu.Unlock()
	s.removeIfEmpty(rm)
}

func (s *Server) publish(ctx context.Context, rm *room, from *peer, ct []byte) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	m, err := s.cfg.Store.Append(ctx, rm.route, from.id, time.Now().UnixMilli(), ct)
	if err != nil {
		return err
	}
	s.messages.Add(1)
	s.fanout(rm, nil, encode(protocol.ServerFrame{
		Type: protocol.TypeMessage, Seq: m.Seq, Peer: m.Peer, TS: m.TS, CT: m.CT,
	}))
	return nil
}

// fanout queues b for every peer in rm except skip. Peers whose queue is
// full are disconnected rather than allowed to stall the room.
// The caller must hold rm.mu.
func (s *Server) fanout(rm *room, skip *peer, b []byte) {
	for p := range rm.peers {
		if p == skip {
			continue
		}
		select {
		case p.out <- b:
		default:
			p.kick(websocket.StatusPolicyViolation, "too slow")
		}
	}
}

func (s *Server) sendError(p *peer, code, msg string) {
	select {
	case p.out <- encode(protocol.ServerFrame{Type: protocol.TypeError, Code: code, Message: msg}):
	default:
		p.kick(websocket.StatusPolicyViolation, "too slow")
	}
}

func encode(f protocol.ServerFrame) []byte {
	b, err := json.Marshal(f)
	if err != nil {
		panic(err) // ServerFrame always marshals
	}
	return b
}
