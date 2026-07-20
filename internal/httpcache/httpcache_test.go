package httpcache

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func write(t *testing.T, ifNoneMatch string, v any) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://example.test/thing", nil)
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	if err := WriteJSON(w, r, "application/json; charset=utf-8", "public, max-age=0, must-revalidate", v); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWriteJSONTagsTheExactBytesItWrites(t *testing.T) {
	payload := map[string]any{"url": "https://example.test/a?b=1&c=2", "n": 3}
	w := write(t, "", payload)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=0, must-revalidate" {
		t.Fatalf("cache-control = %q", got)
	}
	// The ETag must describe the body actually sent, not a re-serialization.
	if got, want := w.Header().Get("ETag"), ETag(w.Body.Bytes()); got != want {
		t.Fatalf("etag = %s, want %s (a digest of the written bytes)", got, want)
	}
	// HTML escaping stays off, so URLs survive intact.
	if body := w.Body.String(); !json.Valid(w.Body.Bytes()) {
		t.Fatalf("body is not valid JSON: %s", body)
	}
	if want := "https://example.test/a?b=1&c=2"; !strings.Contains(w.Body.String(), want) {
		t.Fatalf("body escaped the URL: %s", w.Body.String())
	}
}

func TestWriteJSONChangesETagWhenContentChanges(t *testing.T) {
	first := write(t, "", map[string]any{"n": 1}).Header().Get("ETag")
	second := write(t, "", map[string]any{"n": 2}).Header().Get("ETag")
	if first == second {
		t.Fatal("different payloads produced the same ETag")
	}
	repeat := write(t, "", map[string]any{"n": 1}).Header().Get("ETag")
	if repeat != first {
		t.Fatal("the same payload produced a different ETag")
	}
}

func TestWriteJSONAnswers304WithNoBody(t *testing.T) {
	payload := map[string]any{"n": 1}
	etag := write(t, "", payload).Header().Get("ETag")

	w := write(t, etag, payload)
	if w.Code != http.StatusNotModified {
		t.Fatalf("code = %d, want 304", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", w.Body.String())
	}
	// A 304 still carries the validator and the caching policy.
	if w.Header().Get("ETag") != etag {
		t.Fatalf("304 etag = %q, want %q", w.Header().Get("ETag"), etag)
	}
	if w.Header().Get("Cache-Control") == "" {
		t.Fatal("304 dropped Cache-Control")
	}
}

func TestWriteJSONIgnoresAStaleValidator(t *testing.T) {
	w := write(t, `"0000000000000000000000000000000000"`, map[string]any{"n": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 for a non-matching validator", w.Code)
	}
	if w.Body.Len() == 0 {
		t.Fatal("200 response has no body")
	}
}

func TestMatchesFollowsRFC9110WeakComparison(t *testing.T) {
	const etag = `"abc123"`
	cases := []struct {
		ifNoneMatch string
		want        bool
	}{
		{"", false},
		{`"abc123"`, true},
		{`W/"abc123"`, true},                // weak comparison for GET
		{`"other", "abc123"`, true},         // comma-separated list
		{`  "other" ,  W/"abc123"  `, true}, // optional whitespace
		{"*", true},                         // any current representation
		{`"nope"`, false},
		{`W/"nope"`, false},
		{`"abc123x"`, false}, // no prefix matching
		{`,,`, false},        // malformed, matches nothing
		{`abc123`, false},    // unquoted is not the tag
	}
	for _, c := range cases {
		if got := Matches(c.ifNoneMatch, etag); got != c.want {
			t.Errorf("Matches(%q, %s) = %v, want %v", c.ifNoneMatch, etag, got, c.want)
		}
	}
}

// Vary must accumulate: CORS handling may already have added Origin, and
// overwriting it would let a shared cache serve one origin's response to
// another.
func TestAddVaryIsAdditiveAndDeduplicated(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Vary", "Origin")
	AddVary(w, "Accept")
	AddVary(w, "Accept")
	AddVary(w, "accept") // case-insensitive field names

	values := w.Header().Values("Vary")
	joined := ""
	for i, v := range values {
		if i > 0 {
			joined += ", "
		}
		joined += v
	}
	if !strings.Contains(joined, "Origin") || !strings.Contains(joined, "Accept") {
		t.Fatalf("Vary = %q, want both Origin and Accept", joined)
	}
	if len(values) != 2 {
		t.Fatalf("Vary values = %v, want Accept added exactly once", values)
	}
}

func TestWriteJSONReportsEncodingFailures(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://example.test/thing", nil)
	w := httptest.NewRecorder()
	// Channels cannot be marshalled.
	err := WriteJSON(w, r, "application/json", "", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("want an error for an unmarshallable payload")
	}
	if w.Body.Len() != 0 {
		t.Fatalf("failed encode wrote a body: %q", w.Body.String())
	}
}
