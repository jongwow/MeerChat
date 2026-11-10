package crypt

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func mustKey(t testing.TB, room, pw string) *Key {
	t.Helper()
	k, err := DeriveKey(room, pw, TestParams)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRoundTrip(t *testing.T) {
	k := mustKey(t, "lobby", "hunter2")
	for _, msg := range []string{"", "hi", strings.Repeat("x", 123), strings.Repeat("y", 124), strings.Repeat("z", MaxPlaintext)} {
		ct, err := k.Seal([]byte(msg))
		if err != nil {
			t.Fatal(err)
		}
		pt, err := k.Open(ct)
		if err != nil {
			t.Fatalf("open(%d bytes): %v", len(msg), err)
		}
		if string(pt) != msg {
			t.Fatalf("round trip mismatch for %d bytes", len(msg))
		}
	}
}

func TestSameInputsSameKey(t *testing.T) {
	a := mustKey(t, "lobby", "pw")
	b := mustKey(t, "lobby", "pw")
	if a.Fingerprint() != b.Fingerprint() || a.Route() != b.Route() {
		t.Fatal("derivation is not deterministic")
	}
	ct, _ := a.Seal([]byte("hello"))
	if _, err := b.Open(ct); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPasswordIsAMeerkat(t *testing.T) {
	right := mustKey(t, "lobby", "correct horse")
	wrong := mustKey(t, "lobby", "battery staple")
	if right.Route() != wrong.Route() {
		t.Fatal("route must depend on the room name only, so wrong passwords land in the same room")
	}
	if right.Fingerprint() == wrong.Fingerprint() {
		t.Fatal("fingerprints should differ")
	}
	ct, _ := right.Seal([]byte("secret"))
	if _, err := wrong.Open(ct); !errors.Is(err, ErrOpen) {
		t.Fatalf("expected ErrOpen, got %v", err)
	}
}

func TestCiphertextBoundToRoom(t *testing.T) {
	// Same password, different room: a ciphertext replayed into another room
	// must not open there.
	a := mustKey(t, "room-a", "pw")
	b := mustKey(t, "room-b", "pw")
	ct, _ := a.Seal([]byte("hello"))
	if _, err := b.Open(ct); err == nil {
		t.Fatal("ciphertext opened in a different room")
	}
}

func TestTamperDetected(t *testing.T) {
	k := mustKey(t, "lobby", "pw")
	ct, _ := k.Seal([]byte("hello"))
	for i := range ct {
		bad := bytes.Clone(ct)
		bad[i] ^= 1
		if _, err := k.Open(bad); err == nil {
			t.Fatalf("flipping byte %d went unnoticed", i)
		}
	}
	if _, err := k.Open(ct[:10]); err == nil {
		t.Fatal("truncated ciphertext opened")
	}
}

func TestPaddingHidesLength(t *testing.T) {
	k := mustKey(t, "lobby", "pw")
	a, _ := k.Seal([]byte("hi"))
	b, _ := k.Seal([]byte(strings.Repeat("x", PadBlock-lenPrefix)))
	if len(a) != len(b) {
		t.Fatalf("lengths differ within one pad block: %d vs %d", len(a), len(b))
	}
	c, _ := k.Seal([]byte(strings.Repeat("x", PadBlock-lenPrefix+1)))
	if len(c) == len(a) {
		t.Fatal("expected next block")
	}
}

func TestSealTooLarge(t *testing.T) {
	k := mustKey(t, "lobby", "pw")
	if _, err := k.Seal(make([]byte, MaxPlaintext+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestRouteID(t *testing.T) {
	r := RouteID("lobby")
	if !ValidRouteID(r) {
		t.Fatalf("invalid route %q", r)
	}
	if strings.Contains(r, "lobby") || r == RouteID("Lobby") {
		t.Fatal("route leaks or collides")
	}
	for _, bad := range []string{"", "xyz", strings.Repeat("A", RouteIDLen), strings.Repeat("g", RouteIDLen)} {
		if ValidRouteID(bad) {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestValidateRoomName(t *testing.T) {
	for _, ok := range []string{"lobby", "미어캣", "team-42"} {
		if err := ValidateRoomName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " lobby", "a/b", strings.Repeat("x", MaxRoomName+1), "\xff"} {
		if ValidateRoomName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func FuzzOpen(f *testing.F) {
	k := mustKey(f, "lobby", "pw")
	ct, _ := k.Seal([]byte("seed"))
	f.Add(ct)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = k.Open(data) // must never panic
	})
}

func BenchmarkDeriveKeyDefault(b *testing.B) {
	for b.Loop() {
		if _, err := DeriveKey("lobby", "pw", DefaultParams); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSeal(b *testing.B) {
	k := mustKey(b, "lobby", "pw")
	msg := []byte("a reasonably sized chat message, about this long")
	for b.Loop() {
		if _, err := k.Seal(msg); err != nil {
			b.Fatal(err)
		}
	}
}

// TestKnownAnswers pins the derivations in docs/PROTOCOL.md. If this fails,
// clients built from different versions will no longer understand each other.
func TestKnownAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("Argon2id with default parameters")
	}
	if got := RouteID("lobby"); got != "36779ba0162c12ce43c240b31bbd9cd2" {
		t.Errorf("RouteID(lobby) = %s", got)
	}
	for pw, want := range map[string]string{
		"correct horse battery staple": "4E73-90A0",
		"guess":                        "AE95-29C7",
	} {
		k, err := DeriveKey("lobby", pw, DefaultParams)
		if err != nil {
			t.Fatal(err)
		}
		if k.Fingerprint() != want {
			t.Errorf("fingerprint(lobby, %q) = %s, want %s", pw, k.Fingerprint(), want)
		}
	}
}
