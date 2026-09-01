package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	a := AuthSettings{Enabled: true, Username: "admin", Secret: "test-secret-000"}

	tok := issueSession("admin", a)
	user, ok := validateSession(tok, a)
	if !ok || user != "admin" {
		t.Fatalf("valid token rejected: user=%q ok=%v", user, ok)
	}

	// Tampered signature.
	if _, ok := validateSession(tok+"x", a); ok {
		t.Error("tampered token accepted")
	}

	// Wrong secret.
	if _, ok := validateSession(tok, AuthSettings{Secret: "other"}); ok {
		t.Error("token validated under a different secret")
	}

	// Username mismatch.
	if _, ok := validateSession(tok, AuthSettings{Username: "someoneelse", Secret: a.Secret}); ok {
		t.Error("token accepted for a different username")
	}

	// Malformed.
	for _, bad := range []string{"", "a|b", "a|b|c|d", "not-a-token"} {
		if _, ok := validateSession(bad, a); ok {
			t.Errorf("malformed token %q accepted", bad)
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	a := AuthSettings{Username: "admin", Secret: "s"}
	// Hand-build an already-expired payload with a valid signature.
	payload := "admin\x1f1"
	inner := payload + "\x1f" + sign(payload, a.Secret)
	expired := base64.RawURLEncoding.EncodeToString([]byte(inner))
	if _, ok := validateSession(expired, a); ok {
		t.Error("expired token accepted")
	}
}

func TestHashPassword(t *testing.T) {
	h, err := hashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2") {
		t.Errorf("expected a bcrypt hash, got %q", h)
	}
}
