// Package protocol defines the MeerChat wire format. See docs/PROTOCOL.md.
//
// There are two layers:
//
//   - Frames are JSON objects exchanged over a WebSocket. The server reads and
//     writes these. They carry only routing metadata and opaque ciphertext.
//   - Payloads are JSON objects that live inside the ciphertext. Only clients
//     holding the room key can read them.
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Subprotocol is negotiated with Sec-WebSocket-Protocol. A breaking change to
// either layer bumps it.
const Subprotocol = "meer.v1"

// Limits enforced by both sides.
const (
	// MaxCiphertext bounds a frame's "ct" field.
	MaxCiphertext = 16 * 1024
	// MaxFrame bounds a whole encoded frame (base64 + JSON overhead).
	MaxFrame = MaxCiphertext*4/3 + 1024
	// MaxText is the longest chat message in bytes.
	MaxText = 4000
	// MaxNick is the longest nickname in bytes.
	MaxNick = 32
)

// Frame types.
const (
	// Client to server.
	TypeSend = "send"

	// Server to client.
	TypeWelcome  = "welcome"
	TypeMessage  = "msg"
	TypePresence = "presence"
	TypeError    = "error"
)

// Presence events.
const (
	PresenceJoin  = "join"
	PresenceLeave = "leave"
)

// Error codes.
const (
	ErrBadFrame    = "bad_frame"
	ErrRateLimited = "rate_limited"
	ErrInternal    = "internal"
)

// ClientFrame is sent by clients.
type ClientFrame struct {
	Type string `json:"t"`
	CT   []byte `json:"ct"`
}

// Validate checks a decoded client frame.
func (f *ClientFrame) Validate() error {
	if f.Type != TypeSend {
		return fmt.Errorf("unknown frame type %q", f.Type)
	}
	if len(f.CT) == 0 || len(f.CT) > MaxCiphertext {
		return fmt.Errorf("ciphertext size %d out of range", len(f.CT))
	}
	return nil
}

// DecodeClientFrame parses and validates a client frame.
func DecodeClientFrame(b []byte) (*ClientFrame, error) {
	var f ClientFrame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// ServerFrame is sent by the server. Which fields are set depends on Type.
type ServerFrame struct {
	Type string `json:"t"`

	// welcome
	You   string `json:"you,omitempty"`   // your peer ID for this connection
	Head  uint64 `json:"head,omitempty"`  // latest sequence number in the room
	Peers int    `json:"peers,omitempty"` // connected peers, including you (welcome, presence)

	// msg
	Seq  uint64 `json:"seq,omitempty"`
	Peer string `json:"peer,omitempty"` // sender's peer ID (msg, presence)
	TS   int64  `json:"ts,omitempty"`   // server receive time, unix milliseconds
	CT   []byte `json:"ct,omitempty"`

	// presence
	Event string `json:"ev,omitempty"`

	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"msg,omitempty"`
}

// Payload kinds.
const (
	KindMessage = "msg"
	KindAction  = "me"
	KindJoin    = "join"
	KindLeave   = "leave"
	KindNick    = "nick"
)

// Payload is the plaintext inside a ciphertext.
type Payload struct {
	Kind string `json:"k"`
	ID   string `json:"id"`          // random, used for de-duplication
	Nick string `json:"n"`           // sender's nickname
	Text string `json:"x,omitempty"` // message text, or previous nick for KindNick
	TS   int64  `json:"t"`           // sender clock, unix milliseconds
}

// NewPayload fills in ID and timestamp.
func NewPayload(kind, nick, text string) Payload {
	return Payload{Kind: kind, ID: NewID(), Nick: nick, Text: text, TS: time.Now().UnixMilli()}
}

// Validate checks a decrypted payload. Decryption proves the payload came
// from someone holding the room key, not that they are well behaved.
func (p *Payload) Validate() error {
	switch p.Kind {
	case KindMessage, KindAction, KindJoin, KindLeave, KindNick:
	default:
		return fmt.Errorf("unknown payload kind %q", p.Kind)
	}
	if p.ID == "" || len(p.ID) > 64 {
		return errors.New("bad payload id")
	}
	if err := ValidateNick(p.Nick); err != nil {
		return err
	}
	if len(p.Text) > MaxText || !utf8.ValidString(p.Text) {
		return errors.New("bad payload text")
	}
	return nil
}

// ValidateNick reports whether n is an acceptable nickname.
func ValidateNick(n string) error {
	switch {
	case n == "":
		return errors.New("nickname is empty")
	case len(n) > MaxNick:
		return fmt.Errorf("nickname is longer than %d bytes", MaxNick)
	case !utf8.ValidString(n):
		return errors.New("nickname is not valid UTF-8")
	case strings.ContainsFunc(n, func(r rune) bool { return r < ' ' || r == 0x7f || r == ' ' }):
		return errors.New("nickname must not contain spaces or control characters")
	}
	return nil
}

// NewID returns a random 128-bit hex identifier.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewPeerID returns a short random peer identifier.
func NewPeerID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Envelope is the plaintext inside every ciphertext: a payload signed by
// the sender's device key. The room key proves membership; the signature
// says which device spoke.
type Envelope struct {
	Body []byte `json:"b"`   // the JSON-encoded Payload
	Key  []byte `json:"pk"`  // sender's Ed25519 public key
	Sig  []byte `json:"sig"` // Ed25519 signature over SigningInput(route, Body)
}

// SigningInput is what a sender signs. Binding the route stops a signed
// payload from being replayed into another room with the same password.
func SigningInput(route string, body []byte) []byte {
	const label = "meerchat/v1/signed-payload\x00"
	out := make([]byte, 0, len(label)+len(route)+1+len(body))
	out = append(out, label...)
	out = append(out, route...)
	out = append(out, 0)
	return append(out, body...)
}
