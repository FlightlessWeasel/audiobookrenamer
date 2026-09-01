package api

import (
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type authStatusResponse struct {
	Enabled       bool   `json:"enabled"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	a := s.authSettings()
	resp := authStatusResponse{Enabled: a.Enabled}
	if !a.Enabled {
		resp.Authenticated = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if s.apiKeyOK(r, a) {
		resp.Authenticated = true
	} else if c, err := r.Cookie(sessionCookie); err == nil {
		if user, ok := validateSession(c.Value, a); ok {
			resp.Authenticated = true
			resp.Username = user
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !s.decode(w, r, &req) {
		return
	}
	a := s.authSettings()
	if !a.Enabled {
		writeErr(w, http.StatusBadRequest, "authentication is disabled")
		return
	}
	if req.Username != a.Username ||
		bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(req.Password)) != nil {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	setSessionCookie(w, r, a)
	writeJSON(w, http.StatusOK, authStatusResponse{Enabled: true, Authenticated: true, Username: a.Username})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
		MaxAge:   -1,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// setSessionCookie writes a freshly signed session cookie for a.Username.
func setSessionCookie(w http.ResponseWriter, r *http.Request, a AuthSettings) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    issueSession(a.Username, a),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
		Expires:  time.Now().Add(sessionTTL),
	})
}

// requestIsSecure reports whether the browser reached us over HTTPS, which is
// what decides the cookie's Secure flag.
//
// r.TLS alone is not enough: the deployment the README recommends puts this
// server behind a TLS-terminating reverse proxy, so r.TLS is always nil and the
// session cookie would go out without Secure — sendable in the clear on any
// plain-HTTP request to the same host. X-Forwarded-Proto is therefore honoured
// as well. Trusting that header here is safe in the one direction that matters:
// a forged one can only cause a cookie to be marked Secure when it needn't be,
// which the browser answers by declining to store it over plain HTTP. It can
// never clear the flag on a connection that was genuinely HTTPS.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
