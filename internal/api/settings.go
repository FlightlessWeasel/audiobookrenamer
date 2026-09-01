package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"audiobookrenamer/internal/db"
	"audiobookrenamer/internal/metadata"
)

// settingsView is the shape returned by GET /api/settings. Secrets are never
// echoed back in full. The provider config keys and stored structs live in
// internal/metadata and are reused here rather than redefined.
type settingsView struct {
	AutoMatchThreshold float64                `json:"auto_match_threshold"`
	Audible            metadata.AudibleConfig `json:"audible"`
	GoogleBooks        googleView             `json:"google_books"`
	OpenLibrary        metadata.ToggleConfig  `json:"open_library"`
	Auth               authView               `json:"auth"`
}

// googleView is the API-facing Google Books config: on read the stored API key
// is reduced to a boolean; on write APIKey is accepted but never echoed.
type googleView struct {
	Enabled   bool   `json:"enabled"`
	APIKeySet bool   `json:"api_key_set"`
	APIKey    string `json:"api_key,omitempty"` // write-only
}

// authView is the API-facing auth config. APIKey is populated only in the
// response to the PATCH that generated or rotated it — the key is not stored in
// a retrievable form anywhere else in the API, so this one response is the
// operator's chance to copy it. A GET never includes it.
type authView struct {
	Enabled   bool   `json:"enabled"`
	Username  string `json:"username,omitempty"`
	APIKeySet bool   `json:"api_key_set"`
	APIKey    string `json:"api_key,omitempty"` // show-once
}

// errAuthIncomplete is returned from the patchSettings auth transaction when the
// requested config would enable auth without a username + password.
var errAuthIncomplete = errors.New("username and password are required to enable auth")

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildSettingsView()
	if err != nil {
		writeDBErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// buildSettingsView reads the current settings into the API-facing view. It is
// shared by GET /settings and the PATCH response, which needs to decorate the
// view with the show-once API key before writing it.
func (s *Server) buildSettingsView() (settingsView, error) {
	v := settingsView{
		AutoMatchThreshold: metadata.DefaultAutoMatchThreshold,
		Audible:            metadata.AudibleConfig{Enabled: true, Region: "us"},
		GoogleBooks:        googleView{Enabled: true},
		OpenLibrary:        metadata.ToggleConfig{Enabled: true},
	}
	// GetSetting returns (false, nil) for an absent key, so a missing setting
	// keeps its default above; only a real read failure (I/O, corrupt JSON) is
	// surfaced as a 500 rather than silently serving wrong defaults.
	if _, err := s.DB.GetSetting(metadata.KeyAutoMatchThreshold, &v.AutoMatchThreshold); err != nil {
		return settingsView{}, err
	}
	if _, err := s.DB.GetSetting(metadata.KeyAudible, &v.Audible); err != nil {
		return settingsView{}, err
	}
	var g metadata.GoogleConfig
	ok, err := s.DB.GetSetting(metadata.KeyGoogleBooks, &g)
	if err != nil {
		return settingsView{}, err
	}
	if ok {
		v.GoogleBooks.Enabled = g.Enabled
		v.GoogleBooks.APIKeySet = g.APIKey != ""
	}
	if _, err := s.DB.GetSetting(metadata.KeyOpenLibrary, &v.OpenLibrary); err != nil {
		return settingsView{}, err
	}

	a := s.authSettings()
	v.Auth = authView{Enabled: a.Enabled, Username: a.Username, APIKeySet: a.APIKey != ""}

	return v, nil
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if !s.decode(w, r, &raw) {
		return
	}

	// Validate everything first and collect the non-auth writes, then commit
	// them in a single transaction so a failure can't leave a partial config.
	// The auth block is handled separately, in its own read-modify-write
	// transaction, so concurrent PATCHes can't lost-update it.
	writes := map[string]any{}

	if v, ok := raw["auto_match_threshold"]; ok {
		var f float64
		if err := json.Unmarshal(v, &f); err != nil || f < 0 || f > 1 {
			writeErr(w, http.StatusBadRequest, "auto_match_threshold must be between 0 and 1")
			return
		}
		writes[metadata.KeyAutoMatchThreshold] = f
	}

	if v, ok := raw["audible"]; ok {
		var c metadata.AudibleConfig
		if err := json.Unmarshal(v, &c); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid audible config")
			return
		}
		if c.Region == "" {
			c.Region = "us"
		}
		writes[metadata.KeyAudible] = c
	}

	if v, ok := raw["google_books"]; ok {
		var in googleView
		if err := json.Unmarshal(v, &in); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid google_books config")
			return
		}
		// Read-modify-write inside the committing transaction: a plain
		// enable/disable toggle (in.APIKey == "") must not clobber the stored
		// API key, and a failed/corrupt read of the current value must fail the
		// PATCH rather than silently wiping the key.
		writes[metadata.KeyGoogleBooks] = db.Mutator(func(cur json.RawMessage, loaded bool) (any, error) {
			var g metadata.GoogleConfig
			if loaded {
				if err := json.Unmarshal(cur, &g); err != nil {
					return nil, err
				}
			}
			g.Enabled = in.Enabled
			if in.APIKey != "" {
				g.APIKey = in.APIKey
			}
			return g, nil
		})
	}

	if v, ok := raw["open_library"]; ok {
		var c metadata.ToggleConfig
		if err := json.Unmarshal(v, &c); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid open_library config")
			return
		}
		writes[metadata.KeyOpenLibrary] = c
	}

	var newAPIKey string
	if v, ok := raw["auth"]; ok {
		var ok bool
		if newAPIKey, ok = s.patchAuthSettings(w, r, v); !ok {
			return
		}
	}

	if err := s.DB.SetSettings(writes); err != nil {
		writeDBErr(w, err)
		return
	}

	view, err := s.buildSettingsView()
	if err != nil {
		writeDBErr(w, err)
		return
	}
	// The only time the API key is ever readable: right after it was minted.
	view.Auth.APIKey = newAPIKey
	writeJSON(w, http.StatusOK, view)
}

// patchAuthSettings applies the "auth" object from a settings PATCH inside a
// single read-modify-write transaction. It returns any newly generated API key
// (empty when none was minted) and whether the patch succeeded; on failure it
// has already written the error response.
func (s *Server) patchAuthSettings(w http.ResponseWriter, r *http.Request, v json.RawMessage) (string, bool) {
	// Whether the caller is holding a session cookie has to be decided against
	// the settings as they are now: a credential change rotates the signing
	// secret, and after the write their cookie no longer validates.
	hadSession := false
	if c, err := r.Cookie(sessionCookie); err == nil {
		_, hadSession = validateSession(c.Value, s.authSettings())
	}

	var in struct {
		Enabled  *bool  `json:"enabled"`
		Username string `json:"username"`
		Password string `json:"password"`
		APIKey   string `json:"api_key"`
		Rotate   bool   `json:"rotate_api_key"`
	}
	if err := json.Unmarshal(v, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid auth config")
		return "", false
	}

	// Hash the password before opening the transaction so the (slow) bcrypt
	// call doesn't hold the DB write lock.
	var newHash string
	if in.Password != "" {
		h, err := hashPassword(in.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not hash password")
			return "", false
		}
		newHash = h
	}

	var a AuthSettings
	var newKey string
	err := s.DB.SetSetting(authSettingKey, db.Mutator(func(raw json.RawMessage, loaded bool) (any, error) {
		if loaded {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, err
			}
		}
		prevUsername := a.Username
		if in.Enabled != nil {
			a.Enabled = *in.Enabled
		}
		if in.Username != "" {
			a.Username = in.Username
		}
		if newHash != "" {
			a.PasswordHash = newHash
		}
		if in.APIKey != "" {
			a.APIKey = in.APIKey
		}
		if in.Rotate || (a.Enabled && a.APIKey == "") {
			a.APIKey = randomKey()
			newKey = a.APIKey
		}
		if a.Enabled && (a.Username == "" || a.PasswordHash == "") {
			return nil, errAuthIncomplete
		}
		// A credential change must take every session issued under the old
		// credentials with it. Sessions are stateless signed cookies, so there
		// is no list to revoke — rotating the signing secret invalidates all of
		// them at once. Disabling auth rotates too, so a cookie minted before
		// the gap cannot be replayed when auth is turned back on.
		credentialsChanged := newHash != "" ||
			(in.Username != "" && in.Username != prevUsername) ||
			(in.Enabled != nil && !*in.Enabled)
		if a.Secret == "" || credentialsChanged {
			a.Secret = randomKey() + randomKey()
		}
		return a, nil
	}))
	if errors.Is(err, errAuthIncomplete) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return "", false
	}
	if err != nil {
		writeDBErr(w, err)
		return "", false
	}

	// The secret rotation above invalidated the caller's own cookie along with
	// everyone else's. Re-issue theirs under the new secret so changing your own
	// password doesn't bounce you to the login screen mid-save; every other
	// session stays revoked.
	if hadSession && a.Enabled {
		setSessionCookie(w, r, a)
	}
	return newKey, true
}
