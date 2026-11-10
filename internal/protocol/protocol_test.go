package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeClientFrame(t *testing.T) {
	good, _ := json.Marshal(ClientFrame{Type: TypeSend, CT: []byte("abc")})
	if _, err := DecodeClientFrame(good); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		`{}`,
		`{"t":"send"}`,
		`{"t":"nope","ct":"YWJj"}`,
		`not json`,
	}
	for _, b := range bad {
		if _, err := DecodeClientFrame([]byte(b)); err == nil {
			t.Errorf("%s accepted", b)
		}
	}
	huge, _ := json.Marshal(ClientFrame{Type: TypeSend, CT: make([]byte, MaxCiphertext+1)})
	if _, err := DecodeClientFrame(huge); err == nil {
		t.Error("oversized ciphertext accepted")
	}
	// MaxFrame must admit the largest legal frame.
	largest, _ := json.Marshal(ClientFrame{Type: TypeSend, CT: make([]byte, MaxCiphertext)})
	if len(largest) > MaxFrame {
		t.Fatalf("MaxFrame %d too small for %d", MaxFrame, len(largest))
	}
}

func TestPayloadValidate(t *testing.T) {
	p := NewPayload(KindMessage, "alice", "hi")
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Payload{
		"kind":  {Kind: "x", ID: "1", Nick: "a"},
		"id":    {Kind: KindMessage, Nick: "a"},
		"nick":  {Kind: KindMessage, ID: "1", Nick: "has space"},
		"text":  {Kind: KindMessage, ID: "1", Nick: "a", Text: strings.Repeat("x", MaxText+1)},
		"ctrl":  {Kind: KindMessage, ID: "1", Nick: "a\x1b[31m"},
		"empty": {Kind: KindMessage, ID: "1"},
	}
	for name, c := range cases {
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzDecodeClientFrame(f *testing.F) {
	f.Add([]byte(`{"t":"send","ct":"YWJj"}`))
	f.Add([]byte(`{"t":`))
	f.Fuzz(func(t *testing.T, b []byte) {
		fr, err := DecodeClientFrame(b)
		if err == nil && (len(fr.CT) == 0 || len(fr.CT) > MaxCiphertext) {
			t.Fatal("invalid frame passed validation")
		}
	})
}
