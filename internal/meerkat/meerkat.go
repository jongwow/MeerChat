// Package meerkat turns things you cannot read into things you can hear.
package meerkat

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

var sounds = []string{
	"meer", "meer", "meer", "meeer", "meerr", "mrr", "meer?", "meer!",
	"MEER", "meer meer", "*chirp*", "*sniff*", "mrrp", "meeeer", "*stands up*",
}

// Speak produces deterministic meerkat chatter for an undecryptable
// ciphertext. Longer ciphertexts make for longer speeches, so the noise
// carries exactly as much information as the server already has.
func Speak(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	n := len(ciphertext)/48 + 1
	n = min(max(n, 1), 12)
	words := make([]string, n)
	for i := range n {
		v := binary.BigEndian.Uint16(sum[(i*2)%len(sum):])
		words[i] = sounds[int(v)%len(sounds)]
	}
	return strings.Join(words, " ")
}

// Name is the display name for a peer whose messages cannot be decrypted.
func Name(peer string) string {
	if len(peer) > 4 {
		peer = peer[:4]
	}
	return "meerkat-" + peer
}

// Art is shown in empty rooms.
const Art = `
        .--.
       ( oo )      it's quiet here.
        )  (       say something,
       / '' \      or wait for the
      /|    |\     meerkats to arrive.
       |    |
       ||  ||
      ''    ''
`
