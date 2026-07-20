package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adminGet issues an authenticated admin request against the routed server.
func adminDo(t *testing.T, e *testEnv, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	e.srv.cfg.Admin.Token = "secret"
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	return w
}

func TestAdminPostsListShowsDeletionState(t *testing.T) {
	e := newTestEnv(t)

	w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/posts")
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", w.Code, w.Body.String())
	}
	var rows []struct {
		ID        int64  `json:"id"`
		APID      string `json:"ap_id"`
		DeletedAt string `json:"deleted_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].APID != postAPID || rows[0].DeletedAt != "" {
		t.Fatalf("rows = %+v, want one live post", rows)
	}

	adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")

	w = adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/posts")
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].DeletedAt == "" {
		t.Fatalf("rows after delete = %+v, want a deletion timestamp", rows)
	}
}

func TestAdminPostsRequiresAuthorization(t *testing.T) {
	e := newTestEnv(t)
	e.srv.cfg.Admin.Token = "secret"
	for _, target := range []string{
		"https://ap.vrypan.net/admin/posts",
		"https://ap.vrypan.net/admin/posts/1",
	} {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated code = %d, want 401", target, w.Code)
		}
	}
}

func TestAdminDeletePostIsIdempotent(t *testing.T) {
	e := newTestEnv(t)

	type result struct {
		APID           string `json:"ap_id"`
		DeletedAt      string `json:"deleted_at"`
		AlreadyDeleted bool   `json:"already_deleted"`
		Queued         int    `json:"queued"`
	}

	w := adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")
	if w.Code != http.StatusOK {
		t.Fatalf("first delete code = %d, body %s", w.Code, w.Body.String())
	}
	var first result
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.AlreadyDeleted || first.APID != postAPID || first.DeletedAt == "" {
		t.Fatalf("first delete = %+v", first)
	}

	w = adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")
	if w.Code != http.StatusOK {
		t.Fatalf("repeat delete code = %d, want 200 so retries are safe", w.Code)
	}
	var second result
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyDeleted || second.Queued != 0 {
		t.Fatalf("repeat delete = %+v, want already_deleted with nothing queued", second)
	}
	if second.DeletedAt != first.DeletedAt {
		t.Fatalf("repeat moved deleted_at from %q to %q", first.DeletedAt, second.DeletedAt)
	}
}

func TestAdminDeletePostRejectsUnknownAndNonNumeric(t *testing.T) {
	e := newTestEnv(t)
	for _, target := range []string{
		"https://ap.vrypan.net/admin/posts/9999",
		"https://ap.vrypan.net/admin/posts/not-a-number",
	} {
		w := adminDo(t, e, http.MethodDelete, target)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s code = %d, want 404", target, w.Code)
		}
	}
}

func TestDeletedPostServesTombstoneAndLeavesOutbox(t *testing.T) {
	e := newTestEnv(t)
	adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")

	// ActivityPub clients get 410 with a Tombstone body.
	r := httptest.NewRequest(http.MethodGet, postAPID, nil)
	r.Header.Set("Accept", "application/activity+json")
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusGone {
		t.Fatalf("AP code = %d, want 410", w.Code)
	}
	var tombstone struct {
		Context    string `json:"@context"`
		ID         string `json:"id"`
		Type       string `json:"type"`
		FormerType string `json:"formerType"`
		Deleted    string `json:"deleted"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tombstone); err != nil {
		t.Fatal(err)
	}
	if tombstone.ID != postAPID || tombstone.Type != "Tombstone" ||
		tombstone.FormerType != "Note" || tombstone.Deleted == "" ||
		tombstone.Context != "https://www.w3.org/ns/activitystreams" {
		t.Fatalf("tombstone = %+v", tombstone)
	}

	// Browsers get 410 too, not the instance chooser.
	r = httptest.NewRequest(http.MethodGet, postAPID, nil)
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusGone {
		t.Fatalf("browser code = %d, want 410", w.Code)
	}
	if strings.Contains(w.Body.String(), "authorize_interaction") {
		t.Fatal("browser got the instance chooser for a deleted post")
	}

	// An id that never existed is still 404, not 410.
	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/posts/ffffffffffffffff", nil)
	r.Header.Set("Accept", "application/activity+json")
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown post code = %d, want 404", w.Code)
	}

	// The outbox drops the post from both its total and its pages.
	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/outbox", nil)
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	var collection struct {
		TotalItems int `json:"totalItems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.TotalItems != 0 {
		t.Fatalf("outbox totalItems = %d, want 0", collection.TotalItems)
	}
	r = httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/outbox?page=1", nil)
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	var page struct {
		OrderedItems []any `json:"orderedItems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.OrderedItems) != 0 {
		t.Fatalf("outbox page items = %d, want 0", len(page.OrderedItems))
	}
}

func TestInteractionsForDeletedPostReportNothing(t *testing.T) {
	e := newTestEnv(t)
	postID, ok, err := e.st.ResolvePost(postURL)
	if err != nil || !ok {
		t.Fatalf("resolve post: %v %v", ok, err)
	}
	if _, err := e.st.InsertInteraction(testInteraction(postID, "like", "https://remote.example/likes/1")); err != nil {
		t.Fatal(err)
	}
	adminDo(t, e, http.MethodDelete, "https://ap.vrypan.net/admin/posts/1")

	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/api/interactions?url="+postURL, nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	var payload struct {
		FediverseURL *string `json:"fediverse_url"`
		Likes        int     `json:"likes"`
		Boosts       int     `json:"boosts"`
		Replies      []any   `json:"replies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FediverseURL != nil || payload.Likes != 0 || payload.Boosts != 0 || len(payload.Replies) != 0 {
		t.Fatalf("payload = %+v, want an empty result for a deleted post", payload)
	}

	// The stored interaction itself survives, for moderation and audit.
	stored, err := e.st.VisibleInteractionsForPost(postID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored interactions = %d, want 1", len(stored))
	}
}
