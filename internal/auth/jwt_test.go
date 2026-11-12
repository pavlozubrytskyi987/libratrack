package auth

import (
	"strings"
	"testing"
	"time"
)

var secret = []byte("unit-test-secret")

func sign(t *testing.T, c Claims) string {
	t.Helper()
	tok, err := Sign(secret, c)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestRoundTrip(t *testing.T) {
	now := time.Now()
	tok := sign(t, Claims{Subject: "alice", Role: RoleLibrarian, ExpiresAt: now.Add(time.Hour).Unix()})
	c, err := Verify(secret, tok, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "alice" || c.Role != RoleLibrarian {
		t.Fatalf("claims: %+v", c)
	}
}

func TestExpired(t *testing.T) {
	now := time.Now()
	tok := sign(t, Claims{Subject: "a", Role: RoleReader, ExpiresAt: now.Add(-time.Second).Unix()})
	if _, err := Verify(secret, tok, now); err != ErrExpired {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
}

func TestWrongSecret(t *testing.T) {
	now := time.Now()
	tok := sign(t, Claims{Subject: "a", Role: RoleReader, ExpiresAt: now.Add(time.Hour).Unix()})
	if _, err := Verify([]byte("other"), tok, now); err != ErrSignature {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

func TestTamperedPayload(t *testing.T) {
	now := time.Now()
	tok := sign(t, Claims{Subject: "a", Role: RoleReader, ExpiresAt: now.Add(time.Hour).Unix()})
	parts := strings.Split(tok, ".")
	forged := sign(t, Claims{Subject: "a", Role: RoleLibrarian, ExpiresAt: now.Add(time.Hour).Unix()})
	parts[1] = strings.Split(forged, ".")[1]
	if _, err := Verify(secret, strings.Join(parts, "."), now); err != ErrSignature {
		t.Fatalf("err = %v, want ErrSignature", err)
	}
}

func TestAlgNoneRejected(t *testing.T) {
	tok := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJhIiwicm9sZSI6ImxpYnJhcmlhbiIsImV4cCI6OTk5OTk5OTk5OX0."
	if _, err := Verify(secret, tok, time.Now()); err == nil {
		t.Fatal("alg=none token should be rejected")
	}
}

func TestMalformed(t *testing.T) {
	for _, tok := range []string{"", "abc", "a.b", "a.b.c.d"} {
		if _, err := Verify(secret, tok, time.Now()); err == nil {
			t.Fatalf("%q should be rejected", tok)
		}
	}
}
