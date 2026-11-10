package meerkat

import (
	"strings"
	"testing"
)

func TestSpeakDeterministic(t *testing.T) {
	ct := []byte("some ciphertext bytes")
	if first, second := Speak(ct), Speak(ct); first != second {
		t.Fatal("not deterministic")
	}
	if Speak(ct) == "" {
		t.Fatal("empty speech")
	}
}

func TestSpeakGrowsWithLength(t *testing.T) {
	short := strings.Count(Speak(make([]byte, 10)), " ")
	long := strings.Count(Speak(make([]byte, 400)), " ")
	if long <= short {
		t.Fatalf("expected longer speech for longer ciphertext: %d vs %d", short, long)
	}
}

func TestName(t *testing.T) {
	if got := Name("abcdef12"); got != "meerkat-abcd" {
		t.Fatal(got)
	}
	if got := Name("ab"); got != "meerkat-ab" {
		t.Fatal(got)
	}
}
