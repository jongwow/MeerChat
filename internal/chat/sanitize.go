package chat

import (
	"strings"
	"unicode"
)

// Sanitize makes untrusted text safe to print to a terminal. Anyone holding a
// room key can send arbitrary bytes, and escape sequences in chat messages
// are a classic way to rewrite someone's screen, set their window title, or
// worse. Tabs become spaces, newlines are kept if keepNewlines is set, and
// every other control or format character is dropped.
func Sanitize(s string, keepNewlines bool) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' && keepNewlines:
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r) && r != '‍':
			// Cf covers bidi overrides (Trojan Source style tricks). Zero
			// width joiner is kept so emoji sequences survive.
			return -1
		}
		return r
	}, s)
}
