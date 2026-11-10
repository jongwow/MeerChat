package chat

import "testing"

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"hello":                   "hello",
		"\x1b[2J\x1b]0;pwned\x07": "[2J]0;pwned",
		"a\tb":                    "a b",
		"line1\nline2":            "line1line2",
		"\u202eevil":              "evil",
		"👩‍💻":                     "👩‍💻",
		"미어캣\r":                   "미어캣",
	}
	for in, want := range cases {
		if got := Sanitize(in, false); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Sanitize("a\nb", true); got != "a\nb" {
		t.Errorf("newline not kept: %q", got)
	}
}
