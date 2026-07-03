package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func testInteraction(postID int64, kind, apID string) *store.Interaction {
	return &store.Interaction{
		APID: apID, Kind: kind, PostID: postID, ActorID: remoteActorID,
		ActorHandle: "alice@remote.example", ActorName: "Alice", Published: "2026-07-04T10:00:00Z",
		ContentHTML: "<p>hello</p>",
	}
}
