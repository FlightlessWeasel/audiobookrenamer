package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// enableAuth turns auth on with the given credentials and returns the response.
func enableAuth(t *testing.T, s *Server, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"auth":{"enabled":true,"username":"` + user + `","password":"` + pass + `"}}`
	rr := patchSettings(t, s, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable auth: status %d (body: %s)", rr.Code, rr.Body.String())
	}
	return rr
}

// Sessions are stateless signed cookies, so there is no per-session record to
// revoke. Changing the password therefore has to rotate the signing secret, or
// a stolen 30-day cookie keeps full access to organize/apply and library
// deletion after the operator has "locked the attacker out".
func TestPatchAuth_PasswordChangeInvalidatesExistingSessions(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, "admin", "first-password")

	before := s.authSettings()
	cookie := issueSession("admin", before)
	if _, ok := validateSession(cookie, s.authSettings()); !ok {
		t.Fatal("precondition: freshly issued session should validate")
	}

	rr := patchSettings(t, s, `{"auth":{"enabled":true,"password":"second-password"}}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("change password: status %d (body: %s)", rr.Code, rr.Body.String())
	}

	if _, ok := validateSession(cookie, s.authSettings()); ok {
		t.Error("session issued under the old password still validates after the change")
	}
	if after := s.authSettings(); after.Secret == before.Secret {
		t.Error("signing secret was not rotated by the password change")
	}
}

// Turning auth off must rotate too, so a cookie minted before the gap cannot be
// replayed once auth is turned back on.
func TestPatchAuth_DisablingRotatesTheSigningSecret(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, "admin", "first-password")
	cookie := issueSession("admin", s.authSettings())

	if rr := patchSettings(t, s, `{"auth":{"enabled":false}}`); rr.Code != http.StatusOK {
		t.Fatalf("disable auth: status %d (body: %s)", rr.Code, rr.Body.String())
	}
	if rr := patchSettings(t, s, `{"auth":{"enabled":true}}`); rr.Code != http.StatusOK {
		t.Fatalf("re-enable auth: status %d (body: %s)", rr.Code, rr.Body.String())
	}

	if _, ok := validateSession(cookie, s.authSettings()); ok {
		t.Error("session from before auth was disabled still validates")
	}
}

// Rotating the secret logs everyone out including the operator who made the
// change. Their own request re-issues a cookie under the new secret so the save
// doesn't bounce them to the login screen.
func TestPatchAuth_ReissuesTheCallersOwnSession(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, "admin", "first-password")

	req := httptest.NewRequest(http.MethodPatch, "/api/settings",
		strings.NewReader(`{"auth":{"enabled":true,"password":"second-password"}}`))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: issueSession("admin", s.authSettings())})
	rr := httptest.NewRecorder()
	s.patchSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d (body: %s)", rr.Code, rr.Body.String())
	}

	var fresh string
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			fresh = c.Value
		}
	}
	if fresh == "" {
		t.Fatal("no replacement session cookie was set")
	}
	if _, ok := validateSession(fresh, s.authSettings()); !ok {
		t.Error("the re-issued cookie does not validate under the new secret")
	}
}

// The generated API key is not stored anywhere the API reads back, so the
// response to the PATCH that mints it is the operator's only chance to copy it.
// A later GET must not carry it.
func TestPatchAuth_ReturnsGeneratedAPIKeyExactlyOnce(t *testing.T) {
	s := newTestServer(t)
	rr := enableAuth(t, s, "admin", "first-password")

	var created settingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Auth.APIKey == "" {
		t.Fatal("enabling auth generated an API key but did not return it")
	}
	if !created.Auth.APIKeySet {
		t.Error("api_key_set = false after a key was generated")
	}

	getRR := httptest.NewRecorder()
	s.getSettings(getRR, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	var fetched settingsView
	if err := json.Unmarshal(getRR.Body.Bytes(), &fetched); err != nil {
		t.Fatal(err)
	}
	if fetched.Auth.APIKey != "" {
		t.Error("GET /settings leaked the API key; it must only be returned when minted")
	}

	// Rotating mints a new one and returns that.
	rotRR := patchSettings(t, s, `{"auth":{"rotate_api_key":true}}`)
	var rotated settingsView
	if err := json.Unmarshal(rotRR.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Auth.APIKey == "" {
		t.Error("rotation did not return the new key")
	}
	if rotated.Auth.APIKey == created.Auth.APIKey {
		t.Error("rotation returned the same key")
	}
}

// The documented deployment terminates TLS at a reverse proxy, so r.TLS is nil
// on every request. Deciding Secure from r.TLS alone ships the session cookie
// without it, and any plain-HTTP request to the same host then sends it in the
// clear.
func TestLogin_MarksCookieSecureBehindTLSProxy(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, "admin", "hunter2hunter2")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"hunter2hunter2"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rr := httptest.NewRecorder()
	s.login(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: status %d (body: %s)", rr.Code, rr.Body.String())
	}

	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			if !c.Secure {
				t.Error("session cookie is missing Secure behind X-Forwarded-Proto: https")
			}
			return
		}
	}
	t.Fatal("login set no session cookie")
}

// Over plain HTTP with no proxy header, Secure must stay off or the browser
// would refuse to store the cookie and login would silently never stick.
func TestLogin_LeavesCookieInsecureOnPlainHTTP(t *testing.T) {
	s := newTestServer(t)
	enableAuth(t, s, "admin", "hunter2hunter2")

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login",
		strings.NewReader(`{"username":"admin","password":"hunter2hunter2"}`))
	rr := httptest.NewRecorder()
	s.login(rr, req)

	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie && c.Secure {
			t.Error("session cookie marked Secure on a plain-HTTP request")
		}
	}
}
