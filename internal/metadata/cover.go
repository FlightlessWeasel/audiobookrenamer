package metadata

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// maxCoverBytes bounds a fetched cover image. Providers serve a thumbnail or a
// single cover page, never anything approaching this; the cap exists so a
// misbehaving or malicious URL can't be used to fill the database.
const maxCoverBytes = 10 << 20 // 10 MiB

// FetchImage downloads reqURL — a candidate's cover_url — and returns its bytes
// and a normalized MIME type ("image/jpeg" or "image/png"). It shares GetJSON's
// per-host throttle and user agent but performs no caching of its own: the
// caller is the one keeping a durable copy (keyed by book, not by URL), and
// re-fetching the same URL is cheap and rare (once per accepted match).
//
// The returned MIME is sniffed from the image bytes rather than trusted from
// the response's Content-Type header, which providers and CDNs sometimes leave
// generic ("application/octet-stream") or wrong.
func (c *Client) FetchImage(ctx context.Context, reqURL string) (data []byte, mime string, err error) {
	data, headerType, err := c.get(ctx, reqURL, "image/*", nil, maxCoverBytes)
	if err != nil {
		return nil, "", err
	}
	mime = sniffImageMIME(data)
	if mime == "" {
		mime = normalizeImageMIME(headerType)
	}
	if mime == "" {
		return nil, "", fmt.Errorf("%s: not a recognised JPEG or PNG image", reqURL)
	}
	return data, mime, nil
}

var (
	pngMagic  = []byte("\x89PNG\r\n\x1a\n")
	jpegMagic = []byte("\xff\xd8\xff")
)

// sniffImageMIME identifies data by its magic bytes, returning "" for anything
// but JPEG or PNG — the two formats every writer in internal/tagwrite embeds.
func sniffImageMIME(data []byte) string {
	switch {
	case bytes.HasPrefix(data, pngMagic):
		return "image/png"
	case bytes.HasPrefix(data, jpegMagic):
		return "image/jpeg"
	default:
		return ""
	}
}

// normalizeImageMIME maps a Content-Type header value to "image/jpeg" or
// "image/png", or "" when it names neither.
func normalizeImageMIME(contentType string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "png"):
		return "image/png"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
		return "image/jpeg"
	default:
		return ""
	}
}
