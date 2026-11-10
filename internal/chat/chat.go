// Package chat holds the types shared by the client, daemon, CLI and TUI.
package chat

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jongwow/meerchat/internal/crypt"
)

// DefaultPort is the relay's default port. M-E-E-R on a phone keypad.
const DefaultPort = "6337"

// Event kinds delivered to subscribers.
const (
	KindMessage = "message" // a chat message
	KindAction  = "action"  // /me
	KindJoin    = "join"    // someone announced themselves
	KindLeave   = "leave"   // someone said goodbye
	KindNick    = "nick"    // someone changed nickname
	KindMeerkat = "meerkat" // a ciphertext we could not decrypt
	KindStatus  = "status"  // our connection state changed
	KindPeers   = "peers"   // connected peer count changed
	KindSystem  = "system"  // local notices (joined, left, errors)
	KindSync    = "sync"    // end of backlog; live events follow
)

// Connection states.
const (
	StatusConnecting = "connecting"
	StatusOnline     = "online"
	StatusOffline    = "offline"
)

// Event is one thing that happened in a room.
type Event struct {
	Room    string    `json:"room"`
	Kind    string    `json:"kind"`
	Time    time.Time `json:"time"`
	Seq     uint64    `json:"seq,omitempty"`
	Peer    string    `json:"peer,omitempty"`
	Nick    string    `json:"nick,omitempty"`
	Text    string    `json:"text,omitempty"`
	Self    bool      `json:"self,omitempty"`
	History bool      `json:"history,omitempty"`
	Peers   int       `json:"peers,omitempty"`
	Status  string    `json:"status,omitempty"`
	// Key is the sender's device key fingerprint and Trust how it compares
	// with what this nickname used before: self, new, known or changed.
	Key   string `json:"key,omitempty"`
	Trust string `json:"trust,omitempty"`
}

// RoomInfo describes a joined room.
type RoomInfo struct {
	URL         string `json:"url"`
	Name        string `json:"name"`
	Server      string `json:"server"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Peers       int    `json:"peers"`
}

// Address identifies a room on a relay.
type Address struct {
	TLS  bool
	Host string // host:port
	Room string
}

// ParseAddress accepts
//
//	meer://host[:port]/room    plain WebSocket, default port 6337
//	meers://host[:port]/room   WebSocket over TLS, default port 443
//	host[:port]/room           same as meer://
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "meer://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return Address{}, fmt.Errorf("invalid room address: %w", err)
	}
	var a Address
	port := DefaultPort
	switch u.Scheme {
	case "meer":
	case "meers":
		a.TLS, port = true, "443"
	default:
		return Address{}, fmt.Errorf("unknown scheme %q (use meer:// or meers://)", u.Scheme)
	}
	if u.Hostname() == "" {
		return Address{}, errors.New("room address has no host")
	}
	if u.Port() != "" {
		port = u.Port()
	}
	a.Host = net.JoinHostPort(u.Hostname(), port)
	a.Room = strings.TrimPrefix(u.Path, "/")
	if a.Room == "" {
		return Address{}, errors.New("room address has no room name, e.g. meer://example.com/lobby")
	}
	if err := crypt.ValidateRoomName(a.Room); err != nil {
		return Address{}, err
	}
	return a, nil
}

// String renders the canonical form, omitting default ports.
func (a Address) String() string {
	scheme, def := "meer", DefaultPort
	if a.TLS {
		scheme, def = "meers", "443"
	}
	host := a.Host
	if h, p, err := net.SplitHostPort(a.Host); err == nil && p == def {
		host = h
		if strings.Contains(h, ":") {
			host = "[" + h + "]"
		}
	}
	return scheme + "://" + host + "/" + a.Room
}

// WebSocketURL returns the relay endpoint for this room's route.
func (a Address) WebSocketURL(route string) string {
	scheme := "ws"
	if a.TLS {
		scheme = "wss"
	}
	return scheme + "://" + a.Host + "/v1/rooms/" + route + "/ws"
}
