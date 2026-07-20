package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/store"
)

// apGet fetches a path as an ActivityPub client would.
func apGet(t *testing.T, e *testEnv, target, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Header.Set("Accept", "application/activity+json")
	if ifNoneMatch != "" {
		r.Header.Set("If-None-Match", ifNoneMatch)
	}
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	return w
}

// etagOf fetches a document and returns its validator, asserting a 200.
func etagOf(t *testing.T, e *testEnv, target string) string {
	t.Helper()
	w := apGet(t, e, target, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%s: code = %d, want 200", target, w.Code)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("%s: no ETag header", target)
	}
	return etag
}

var apPaths = []string{
	"https://ap.vrypan.net/actor",
	"https://ap.vrypan.net/posts/abcdef0123456789",
	"https://ap.vrypan.net/outbox",
	"https://ap.vrypan.net/outbox?page=1",
	"https://ap.vrypan.net/followers",
}

func TestActivityPubDocumentsRevalidateTo304(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range apPaths {
		first := apGet(t, e, target, "")
		if first.Code != http.StatusOK {
			t.Fatalf("%s: code = %d, want 200", target, first.Code)
		}
		if got := first.Header().Get("Content-Type"); got != ap.ContentType {
			t.Fatalf("%s: content-type = %q, want %q", target, got, ap.ContentType)
		}
		if got := first.Header().Get("Cache-Control"); got != ap.CacheControl {
			t.Fatalf("%s: cache-control = %q, want %q", target, got, ap.CacheControl)
		}
		etag := first.Header().Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag header", target)
		}

		second := apGet(t, e, target, etag)
		if second.Code != http.StatusNotModified {
			t.Fatalf("%s: revalidation code = %d, want 304", target, second.Code)
		}
		if second.Body.Len() != 0 {
			t.Fatalf("%s: 304 body = %q, want empty", target, second.Body.String())
		}

		// A weak validator and a wildcard must revalidate too.
		if w := apGet(t, e, target, "W/"+etag); w.Code != http.StatusNotModified {
			t.Fatalf("%s: weak validator code = %d, want 304", target, w.Code)
		}
		if w := apGet(t, e, target, "*"); w.Code != http.StatusNotModified {
			t.Fatalf("%s: wildcard code = %d, want 304", target, w.Code)
		}
	}
}

// Paths that answer differently to browsers must tell caches so.
func TestNegotiatedPathsVaryOnAccept(t *testing.T) {
	e := newTestEnv(t)
	negotiated := []string{
		"https://ap.vrypan.net/actor",
		"https://ap.vrypan.net/posts/abcdef0123456789",
	}
	for _, target := range negotiated {
		if got := apGet(t, e, target, "").Header().Get("Vary"); !strings.Contains(got, "Accept") {
			t.Fatalf("%s: AP response Vary = %q, want it to include Accept", target, got)
		}
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		if got := w.Header().Get("Vary"); !strings.Contains(got, "Accept") {
			t.Fatalf("%s: browser response Vary = %q, want it to include Accept", target, got)
		}
	}
}

// A browser must never receive the ActivityPub validator for these paths;
// otherwise a cache could hand the JSON to a browser or vice versa.
func TestBrowserRepresentationsCarryNoActivityPubETag(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{
		"https://ap.vrypan.net/actor",
		"https://ap.vrypan.net/posts/abcdef0123456789",
	} {
		apETag := etagOf(t, e, target)
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		if w.Header().Get("ETag") == apETag {
			t.Fatalf("%s: browser response reused the ActivityPub ETag", target)
		}
	}
	// The actor still redirects browsers to the blog.
	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/actor", nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("browser actor code = %d, want 302", w.Code)
	}
}

func TestActorETagTracksProfileChanges(t *testing.T) {
	e := newTestEnv(t)
	const target = "https://ap.vrypan.net/actor"
	before := etagOf(t, e, target)

	e.srv.ap.Actor.Summary = "A new bio"
	after := etagOf(t, e, target)
	if after == before {
		t.Fatal("actor ETag did not change after a profile change")
	}
	if w := apGet(t, e, target, before); w.Code != http.StatusOK {
		t.Fatalf("stale validator code = %d, want 200", w.Code)
	}

	// The public key is part of the representation too.
	e.srv.ap.PublicKeyPEM = "-----BEGIN PUBLIC KEY-----\nROTATED\n-----END PUBLIC KEY-----\n"
	if rotated := etagOf(t, e, target); rotated == after {
		t.Fatal("actor ETag did not change after a public key change")
	}
}

func TestPostETagTracksUpdateAndTombstone(t *testing.T) {
	e := newTestEnv(t)
	const target = postAPID
	before := etagOf(t, e, target)

	if err := e.st.UpdatePostContent("guid-1", "Hello again", "<p>more</p>",
		"newhash", "2026-07-20T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	updated := etagOf(t, e, target)
	if updated == before {
		t.Fatal("post ETag did not change after a content update")
	}

	// A Tombstone is a different representation, so it needs a new validator.
	adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")
	gone := apGet(t, e, target, "")
	if gone.Code != http.StatusGone {
		t.Fatalf("deleted post code = %d, want 410", gone.Code)
	}
	if tag := gone.Header().Get("ETag"); tag == "" || tag == updated {
		t.Fatalf("Tombstone ETag = %q, want a new validator of its own", tag)
	}
	// The old validator must not revalidate against the Tombstone.
	if w := apGet(t, e, target, updated); w.Code == http.StatusNotModified {
		t.Fatal("a stale Note validator revalidated against a Tombstone")
	}
}

func TestOutboxAndFollowersETagsTrackTheirCounts(t *testing.T) {
	e := newTestEnv(t)
	const outbox = "https://ap.vrypan.net/outbox"
	const followers = "https://ap.vrypan.net/followers"

	outboxBefore := etagOf(t, e, outbox)
	pageBefore := etagOf(t, e, outbox+"?page=1")
	followersBefore := etagOf(t, e, followers)

	if _, err := e.st.InsertPost(&store.Post{
		GUID: "guid-2", URL: "https://blog.vrypan.net/2026/07/second/", Title: "Second",
		PublishedAt: "2026-07-02T00:00:00Z",
		APID:        store.NullString("https://ap.vrypan.net/posts/1111111111111111"),
	}); err != nil {
		t.Fatal(err)
	}
	if etagOf(t, e, outbox) == outboxBefore {
		t.Fatal("outbox ETag did not change after a new post")
	}
	if etagOf(t, e, outbox+"?page=1") == pageBefore {
		t.Fatal("outbox page ETag did not change after a new post")
	}
	// A new post is not a new follower.
	if etagOf(t, e, followers) != followersBefore {
		t.Fatal("followers ETag changed because of an unrelated post")
	}

	if err := e.st.UpsertFollower("https://remote.example/users/bob",
		"https://remote.example/users/bob/inbox", ""); err != nil {
		t.Fatal(err)
	}
	if etagOf(t, e, followers) == followersBefore {
		t.Fatal("followers ETag did not change after a new follower")
	}
}
