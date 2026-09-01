package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"audiobookrenamer/internal/db"

	"golang.org/x/crypto/bcrypt"
)

// AuthSettings is the JSON stored under the "auth" settings key. Auth is
// disabled by default; enabling it requires a username + bcrypt password hash
// and exposes an API key for automation. Secret signs session cookies and is
// generated on first use.
type AuthSettings struct {
	Enabled      bool   `json:"enabled"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"` // bcrypt
	APIKey       string `json:"api_key,omitempty"`
	Secret       string `json:"secret,omitempty"` // hex, HMAC key for session cookies
}

const (
	authSettingKey = "auth"
	sessionCookie  = "abr_session"
	sessionTTL     = 30 * 24 * time.Hour
	// streamJobsPath is the SSE job-event endpoint. A browser EventSource can't
	// set request headers, so this is the one route that also accepts the API
	// key as a query parameter (see apiKeyOK). Kept in sync with the route
	// mounted in router.go and the timeout exclusion there.
	streamJobsPath = "/api/jobs/stream"
)

// randomKey returns a 32-hex-char cryptographically-random token. A failure of
// the system RNG is unrecoverable for an auth system, so it panics rather than
// falling back to a predictable value; middleware.Recoverer turns that into a
// 500 for the request that triggered it.
func randomKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func hashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(h), err
}

// loadOrCreateAuthSecret returns the persisted session-signing secret,
// generating and persisting one in a single transaction if none exists yet. It
// is called exactly once at startup so the request path never has to write it.
func loadOrCreateAuthSecret(database *db.DB) (string, error) {
	var a AuthSettings
	err := database.SetSetting(authSettingKey, db.Mutator(func(raw json.RawMessage, loaded bool) (any, error) {
		if loaded {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
		}
		if a.Secret != "" {
			return nil, nil // already have one; no write
		}
		a.Secret = randomKey() + randomKey()
		return a, nil
	}))
	if err != nil {
		return "", err
	}
	if a.Secret == "" {
		return "", errors.New("auth secret not initialized")
	}
	return a.Secret, nil
}

// authSettings loads the stored auth config, including the session-signing
// secret. The secret is read from the row rather than from a cached copy
// because a credential change rotates it (see patchAuthSettings) and every
// session signed with the previous secret must stop validating immediately —
// a cached value would keep honouring them for the life of the process.
//
// The startup-generated secret is only a fallback for the case where the row
// somehow carries none; this function still never writes to the DB.
func (s *Server) authSettings() AuthSettings {
	var a AuthSettings
	if _, err := s.DB.GetSetting(authSettingKey, &a); err != nil {
		a = AuthSettings{}
	}
	if a.Secret == "" {
		a.Secret = s.authSecret
	}
	return a
}

// publicAPIPaths are the exact request paths that stay reachable even when auth
// is enabled, so an operator locked out of the UI can still authenticate.
var publicAPIPaths = map[string]bool{
	"/api/auth/status": true,
	"/api/auth/login":  true,
	"/api/auth/logout": true,
	"/api/healthz":     true,
}

// authMiddleware enforces auth on /api routes when enabled. A valid session
// cookie or X-Api-Key header passes. Everything is allowed when auth is
// disabled.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := s.authSettings()
		if !a.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		// A fixed allowlist of exact paths stays public; a suffix match here
		// would let any route ending in "/auth/login" bypass auth.
		if publicAPIPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if s.apiKeyOK(r, a) {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if _, ok := validateSession(c.Value, a); ok {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="audiobookrenamer"`)
		writeErr(w, http.StatusUnauthorized, "authentication required")
	})
}

func (s *Server) apiKeyOK(r *http.Request, a AuthSettings) bool {
	if a.APIKey == "" {
		return false
	}
	key := r.Header.Get("X-Api-Key")
	// A key in the query string leaks into access logs, browser history, and
	// Referer headers, so it is only honoured for the SSE job-event stream —
	// the one route a browser EventSource reaches, and EventSource cannot set
	// the X-Api-Key header.
	if key == "" && r.URL.Path == streamJobsPath {
		key = r.URL.Query().Get("apikey")
	}
	return key != "" && subtle.ConstantTimeCompare([]byte(key), []byte(a.APIKey)) == 1
}

// issueSession builds a signed token. The inner form is "user\x1fexpiry\x1fsig";
// the whole thing is base64url-encoded so the cookie value is a single opaque
// token with no separator characters.
func issueSession(username string, a AuthSettings) string {
	payload := username + "\x1f" + strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	inner := payload + "\x1f" + sign(payload, a.Secret)
	return base64.RawURLEncoding.EncodeToString([]byte(inner))
}

// validateSession verifies the signature and expiry, returning the username.
func validateSession(token string, a AuthSettings) (string, bool) {
	if a.Secret == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", false
	}
	parts := strings.Split(string(raw), "\x1f")
	if len(parts) != 3 {
		return "", false
	}
	payload := parts[0] + "\x1f" + parts[1]
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(sign(payload, a.Secret))) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	if a.Username != "" && parts[0] != a.Username {
		return "", false
	}
	return parts[0], true
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
