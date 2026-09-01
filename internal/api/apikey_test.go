package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// enableAPIKeyAuth turns on auth with a known API key.
func enableAPIKeyAuth(t *testing.T, s *Server, key string) {
	t.Helper()
	if err := s.DB.SetSetting(authSettingKey, AuthSettings{
		Enabled:      true,
		Username:     "admin",
		PasswordHash: "$2a$10$doesnotmatterforapikeypaths",
		APIKey:       key,
	}); err != nil {
		t.Fatalf("seed auth settings: %v", err)
	}
}

// A browser EventSource can't set request headers, so the SSE job-event stream
// must accept the API key as ?apikey=<key>. This is the only route that does.
func TestAuthMiddleware_APIKeyQueryParamOnStream(t *testing.T) {
	s := newTestServer(t)
	const key = "test-api-key-123"
	enableAPIKeyAuth(t, s, key)

	var reached bool
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("stream accepts ?apikey", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, streamJobsPath+"?apikey="+key, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !reached {
			t.Fatalf("stream with ?apikey should pass auth: code=%d reached=%v", rr.Code, reached)
		}
	})

	t.Run("stream rejects wrong ?apikey", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, streamJobsPath+"?apikey=wrong", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized || reached {
			t.Fatalf("stream with wrong ?apikey should be rejected: code=%d reached=%v", rr.Code, reached)
		}
	})

	t.Run("non-stream route ignores ?apikey", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/api/books?apikey="+key, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized || reached {
			t.Fatalf("non-stream route must not honour ?apikey: code=%d reached=%v", rr.Code, reached)
		}
	})

	t.Run("header still works everywhere", func(t *testing.T) {
		reached = false
		req := httptest.NewRequest(http.MethodGet, "/api/books", nil)
		req.Header.Set("X-Api-Key", key)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || !reached {
			t.Fatalf("X-Api-Key header should pass auth: code=%d reached=%v", rr.Code, reached)
		}
	})
}
