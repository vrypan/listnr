// Package httpcache gives JSON responses exact-representation validators:
// an ETag computed over the very bytes that would be written, and conditional
// handling of If-None-Match.
//
// The package is deliberately protocol-agnostic. Callers supply the content
// type and cache-control policy; nothing here knows about ActivityPub.
package httpcache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// WriteJSON serializes v once, tags it with a strong ETag over the exact
// response bytes, and either writes those bytes or answers a bodyless 304.
//
// Serializing before hashing is the point: the digest can never describe
// anything other than what the client receives. HTML escaping stays off to
// match how the rest of the codebase emits JSON, so URLs in payloads are not
// rewritten into < escapes.
//
// An encoding failure is returned before any header is committed, leaving the
// caller free to answer 500.
func WriteJSON(w http.ResponseWriter, r *http.Request, contentType, cacheControl string, v any) error {
	body, etag, err := prepare(w, contentType, cacheControl, v)
	if err != nil {
		return err
	}
	if Matches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	_, err = w.Write(body)
	return err
}

// WriteJSONStatus writes a JSON representation under a caller-chosen status
// code. It tags the response but never substitutes a 304: per RFC 9110 a 304
// may only stand in for a response that would otherwise have been 200, so a
// 410 Tombstone is always sent in full.
func WriteJSONStatus(w http.ResponseWriter, status int, contentType, cacheControl string, v any) error {
	body, _, err := prepare(w, contentType, cacheControl, v)
	if err != nil {
		return err
	}
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

// prepare serializes v, commits the representation headers, and returns the
// bytes alongside their validator. Nothing is written to the body, so an
// encoding failure still leaves the caller free to answer 500.
func prepare(w http.ResponseWriter, contentType, cacheControl string, v any) ([]byte, string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, "", err
	}
	body := buf.Bytes()

	h := w.Header()
	h.Set("Content-Type", contentType)
	if cacheControl != "" {
		h.Set("Cache-Control", cacheControl)
	}
	etag := ETag(body)
	h.Set("ETag", etag)
	return body, etag, nil
}

// ETag returns a quoted strong validator for exactly these bytes. Half of the
// SHA-256 is ample to make an accidental collision impossible in practice
// while keeping the header short.
func ETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Matches reports whether an If-None-Match header covers etag.
//
// RFC 9110 requires weak comparison for GET, so a "W/"-prefixed tag matches
// its strong counterpart. The header may be a comma-separated list with
// optional whitespace, or "*" meaning any current representation.
func Matches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	etag = strings.TrimPrefix(etag, "W/")
	for _, tok := range strings.Split(ifNoneMatch, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if tok == "*" || strings.TrimPrefix(tok, "W/") == etag {
			return true
		}
	}
	return false
}

// AddVary appends a field name to Vary without disturbing fields already
// there. Header.Set would silently drop an existing "Origin" added by CORS
// handling, which would make a shared cache serve one origin's response to
// another.
func AddVary(w http.ResponseWriter, field string) {
	// Vary may already be present as several header lines, each holding a
	// comma-separated list, so both levels have to be scanned.
	for _, line := range w.Header().Values("Vary") {
		for _, existing := range strings.Split(line, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), field) {
				return
			}
		}
	}
	w.Header().Add("Vary", field)
}
