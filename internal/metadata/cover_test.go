package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

var (
	tinyPNGBytes  = append([]byte("\x89PNG\r\n\x1a\n"), []byte("not really png data but has the magic")...)
	tinyJPEGBytes = append([]byte("\xff\xd8\xff\xe0"), []byte("not really jpeg data but has the magic")...)
)

func TestFetchImage_SniffsMagicBytesOverHeader(t *testing.T) {
	d := testDB(t)
	// A deliberately wrong Content-Type: the sniff must win.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(tinyPNGBytes)
	}))
	defer srv.Close()

	c := NewClient(d)
	data, mime, err := c.FetchImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if string(data) != string(tinyPNGBytes) {
		t.Fatalf("data mismatch: got %d bytes, want %d", len(data), len(tinyPNGBytes))
	}
}

func TestFetchImage_JPEG(t *testing.T) {
	d := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(tinyJPEGBytes)
	}))
	defer srv.Close()

	c := NewClient(d)
	_, mime, err := c.FetchImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
}

func TestFetchImage_FallsBackToHeaderWhenMagicUnrecognised(t *testing.T) {
	d := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png; charset=binary")
		_, _ = w.Write([]byte("no magic bytes here"))
	}))
	defer srv.Close()

	c := NewClient(d)
	_, mime, err := c.FetchImage(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png (from header)", mime)
	}
}

func TestFetchImage_RejectsUnrecognisedContent(t *testing.T) {
	d := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer srv.Close()

	c := NewClient(d)
	if _, _, err := c.FetchImage(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for non-image content, got nil")
	}
}

func TestFetchImage_UpstreamErrorPropagates(t *testing.T) {
	d := testDB(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(d)
	if _, _, err := c.FetchImage(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for a 404, got nil")
	}
}
