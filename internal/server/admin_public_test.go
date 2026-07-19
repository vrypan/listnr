package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/store"
)

func TestAdminAuthDisabledWrongAndRight(t *testing.T) {
	e := newTestEnv(t)
	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/admin/stats", nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled admin code = %d, want 404", w.Code)
	}

	e.srv.cfg.Admin.Token = "secret"
	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/admin/stats", nil)
	r.Header.Set("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token code = %d, want 401", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/admin/stats", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("right token code = %d, want 200", w.Code)
	}
	var stats struct {
		Build struct {
			Version string `json:"version"`
		} `json:"build"`
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Build.Version == "" || stats.SchemaVersion < 1 {
		t.Fatalf("stats build metadata = %+v", stats)
	}
}

func TestInteractionsPayloadExcludesHidden(t *testing.T) {
	e := newTestEnv(t)
	postID, ok, err := e.st.ResolvePost(postURL)
	if err != nil || !ok {
		t.Fatalf("resolve post: %v %v", ok, err)
	}
	if _, err := e.st.InsertInteraction(testInteraction(postID, "like", "https://remote.example/likes/1")); err != nil {
		t.Fatal(err)
	}
	hidden, err := e.st.InsertInteraction(testInteraction(postID, "like", "https://remote.example/likes/2"))
	if err != nil || !hidden {
		t.Fatalf("hidden insert: %v %v", hidden, err)
	}
	if err := e.st.HideInteraction(2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.InsertInteraction(testInteraction(postID, "reply", "https://remote.example/notes/1")); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/api/interactions?url="+postURL, nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var payload struct {
		FediverseURL string `json:"fediverse_url"`
		Likes        int    `json:"likes"`
		Replies      []any  `json:"replies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FediverseURL != postAPID || payload.Likes != 1 || len(payload.Replies) != 1 {
		t.Fatalf("payload = %+v, want ap url, 1 visible like, 1 reply", payload)
	}
}

func TestInteractionsETagRevalidation(t *testing.T) {
	e := newTestEnv(t)
	postID, _, _ := e.st.ResolvePost(postURL)

	get := func(ifNoneMatch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/api/interactions?url="+postURL, nil)
		if ifNoneMatch != "" {
			r.Header.Set("If-None-Match", ifNoneMatch)
		}
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		return w
	}

	first := get("")
	if first.Code != http.StatusOK {
		t.Fatalf("first: code %d", first.Code)
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag header")
	}

	// Same state → conditional request revalidates to 304 with no body.
	second := get(etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("revalidate: code %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 body = %q, want empty", second.Body.String())
	}

	// A new reaction must change the ETag and stop matching the old one.
	if _, err := e.st.InsertInteraction(testInteraction(postID, "like", "https://remote.example/likes/1")); err != nil {
		t.Fatal(err)
	}
	third := get(etag)
	if third.Code != http.StatusOK {
		t.Fatalf("after new like: code %d, want 200 (etag must change)", third.Code)
	}
	if third.Header().Get("ETag") == etag {
		t.Fatal("ETag did not change after a new reaction")
	}
}

func testInteraction(postID int64, kind, apID string) *store.Interaction {
	return &store.Interaction{
		APID: apID, Kind: kind, PostID: postID, ActorID: remoteActorID,
		ActorHandle: "alice@remote.example", ActorName: "Alice", Published: "2026-07-04T10:00:00Z",
		ContentHTML: "<p>hello</p>",
	}
}

func TestPostInterstitialForBrowsers(t *testing.T) {
	e := newTestEnv(t)

	// Browser (no AP Accept header) gets the instance-chooser page.
	r := httptest.NewRequest(http.MethodGet, postAPID, nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("browser code = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body := w.Body.String()
	for _, want := range []string{"authorize_interaction", postAPID, postURL} {
		if !strings.Contains(body, want) {
			t.Fatalf("interstitial missing %q", want)
		}
	}

	// Fediverse software still gets the Note.
	r = httptest.NewRequest(http.MethodGet, postAPID, nil)
	r.Header.Set("Accept", "application/activity+json")
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("AP code = %d, want 200", w.Code)
	}
	var note struct {
		Context string `json:"@context"`
		ID      string `json:"id"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.ID != postAPID || note.Type != "Note" {
		t.Fatalf("note = %+v", note)
	}
	// Mastodon rejects fetched objects without @context.
	if note.Context != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("standalone note @context = %q", note.Context)
	}
}
