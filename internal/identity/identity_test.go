package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestLoadOrCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "identity")
	a, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("identity not persisted")
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key file mode %v", fi.Mode().Perm())
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{4}(-[0-9a-f]{4}){3}$`).MatchString(a.Fingerprint()) {
		t.Fatalf("fingerprint %q", a.Fingerprint())
	}
	_ = os.WriteFile(path, []byte("garbage"), 0o600)
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("garbage key accepted")
	}
}

func TestSignVerify(t *testing.T) {
	id := Generate()
	sig := id.Sign([]byte("hello"))
	if !Verify(id.Public(), []byte("hello"), sig) {
		t.Fatal("valid signature rejected")
	}
	if Verify(id.Public(), []byte("hellO"), sig) || Verify(Generate().Public(), []byte("hello"), sig) {
		t.Fatal("invalid signature accepted")
	}
	if Verify([]byte("short"), []byte("hello"), sig) || Verify(id.Public(), []byte("hello"), sig[:10]) {
		t.Fatal("malformed input accepted")
	}
}

func TestBook(t *testing.T) {
	alice, mallory := Generate().Public(), Generate().Public()
	b := NewBook()
	if got := b.Observe("r", "alice", alice); got != TrustNew {
		t.Fatal(got)
	}
	if got := b.Observe("r", "alice", alice); got != TrustKnown {
		t.Fatal(got)
	}
	if got := b.Observe("r", "alice", mallory); got != TrustChanged {
		t.Fatal(got)
	}
	// The original key is still the trusted one.
	if got := b.Observe("r", "alice", alice); got != TrustKnown {
		t.Fatal(got)
	}
	// Rooms are separate.
	if got := b.Observe("other", "alice", mallory); got != TrustNew {
		t.Fatal(got)
	}
	// Accepting takes the most recent key.
	b.Observe("r", "alice", mallory)
	if _, err := b.Accept("r", "alice"); err != nil {
		t.Fatal(err)
	}
	if got := b.Observe("r", "alice", mallory); got != TrustKnown {
		t.Fatal(got)
	}
	if _, err := b.Accept("r", "nobody"); err == nil {
		t.Fatal("accepted unseen nick")
	}
}

func TestBookRename(t *testing.T) {
	alice, mallory := Generate().Public(), Generate().Public()
	b := NewBook()
	b.Observe("r", "alice", alice)
	if got := b.Rename("r", "alice", "alicia", alice); got != TrustNew {
		t.Fatal(got)
	}
	if got := b.Observe("r", "alicia", alice); got != TrustKnown {
		t.Fatal("rename did not carry the key over")
	}
	if got := b.Rename("r", "alicia", "x", mallory); got != TrustChanged {
		t.Fatal("rename by a different key should be flagged")
	}
}

func TestBookPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_keys.json")
	alice, mallory := Generate().Public(), Generate().Public()
	b, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	b.Observe("r", "alice", alice)
	b2, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := b2.Observe("r", "alice", mallory); got != TrustChanged {
		t.Fatalf("after reload: %s", got)
	}
}
