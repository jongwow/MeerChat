package chat

import "testing"

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in, canon, host string
		tls             bool
	}{
		{"meer://example.com/lobby", "meer://example.com/lobby", "example.com:6337", false},
		{"example.com/lobby", "meer://example.com/lobby", "example.com:6337", false},
		{"meer://localhost:9000/dev", "meer://localhost:9000/dev", "localhost:9000", false},
		{"meers://chat.example.com/team", "meers://chat.example.com/team", "chat.example.com:443", true},
		{"meers://chat.example.com:8443/team", "meers://chat.example.com:8443/team", "chat.example.com:8443", true},
		{"meer://[::1]/v6", "meer://[::1]/v6", "[::1]:6337", false},
		{"meer://example.com/%EB%AF%B8%EC%96%B4%EC%BA%A3", "meer://example.com/미어캣", "example.com:6337", false},
	}
	for _, c := range cases {
		a, err := ParseAddress(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if a.String() != c.canon || a.Host != c.host || a.TLS != c.tls {
			t.Errorf("%s: got %+v (%s)", c.in, a, a)
		}
	}
	for _, bad := range []string{"", "http://x/y", "meer://x", "meer:///room", "meer://x/a/b"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWebSocketURL(t *testing.T) {
	a, _ := ParseAddress("meers://h/r")
	if got := a.WebSocketURL("abc"); got != "wss://h:443/v1/rooms/abc/ws" {
		t.Fatal(got)
	}
}
