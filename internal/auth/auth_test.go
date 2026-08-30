package auth

import (
	"encoding/hex"
	"testing"
	"time"
)

// RFC 6070-style known answer test for PBKDF2-HMAC-SHA256.
func TestPBKDF2KnownVector(t *testing.T) {
	// P="password" S="salt" c=4096 dkLen=32
	want := "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"
	got := pbkdf2Key([]byte("password"), []byte("salt"), 4096, 32)
	if hex.EncodeToString(got) != want {
		t.Fatalf("pbkdf2 mismatch: got %x want %s", got, want)
	}
}

func TestLoginLogoutValid(t *testing.T) {
	a := New("admin", "s3cret", time.Hour)

	tok, ok := a.Login("admin", "s3cret")
	if !ok || tok == "" {
		t.Fatalf("login failed")
	}
	if !a.Valid(tok) {
		t.Fatalf("token should be valid")
	}
	if _, ok := a.Login("admin", "wrong"); ok {
		t.Fatalf("login with wrong password should fail")
	}
	if _, ok := a.Login("root", "s3cret"); ok {
		t.Fatalf("login with wrong username should fail")
	}
	a.Logout(tok)
	if a.Valid(tok) {
		t.Fatalf("token should be invalid after logout")
	}
}

func TestSessionExpiry(t *testing.T) {
	a := New("admin", "pw", 50*time.Millisecond)
	tok, ok := a.Login("admin", "pw")
	if !ok {
		t.Fatalf("login failed")
	}
	time.Sleep(80 * time.Millisecond)
	if a.Valid(tok) {
		t.Fatalf("expired token should be invalid")
	}
}
