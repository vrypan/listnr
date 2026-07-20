package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vrypan/listnr/internal/store"
)

type publishResult struct {
	Published   bool   `json:"published"`
	Fingerprint string `json:"fingerprint"`
	Queued      int    `json:"queued"`
}

func publishActor(t *testing.T, e *testEnv) publishResult {
	t.Helper()
	w := adminDo(t, e, http.MethodPost, "https://ap.vrypan.net/admin/actor/publish")
	if w.Code != http.StatusOK {
		t.Fatalf("publish code = %d, body %s", w.Code, w.Body.String())
	}
	var result publishResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAdminPublishActorRequiresAuthorization(t *testing.T) {
	e := newTestEnv(t)
	e.srv.cfg.Admin.Token = "secret"
	r := httptest.NewRequest(http.MethodPost, "https://ap.vrypan.net/admin/actor/publish", nil)
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestAdminPublishActorDeduplicatesByProfile(t *testing.T) {
	e := newTestEnv(t)
	if err := e.st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}

	first := publishActor(t, e)
	if !first.Published || first.Queued != 1 || first.Fingerprint == "" {
		t.Fatalf("first publish = %+v", first)
	}

	second := publishActor(t, e)
	if second.Published || second.Queued != 0 {
		t.Fatalf("unchanged publish = %+v, want nothing queued", second)
	}
	if second.Fingerprint != first.Fingerprint {
		t.Fatalf("fingerprint changed without a profile change: %q -> %q",
			first.Fingerprint, second.Fingerprint)
	}

	// Editing the loaded profile makes the next publish a real one.
	e.srv.cfg.Actor.Name = "Renamed"
	e.srv.ap.Actor.Name = "Renamed"
	third := publishActor(t, e)
	if !third.Published || third.Queued != 1 {
		t.Fatalf("changed publish = %+v, want published", third)
	}
	if third.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change after a profile change")
	}
}

// The published activity must carry the full current actor document, not a
// patch, and must reach followers through the durable delivery queue.
func TestAdminPublishActorQueuesFullActorDocument(t *testing.T) {
	e := newTestEnv(t)
	if err := e.st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}
	publishActor(t, e)

	due, err := e.st.DueDeliveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("queued deliveries = %d, want 1", len(due))
	}
	var activity struct {
		Type   string `json:"type"`
		Actor  string `json:"actor"`
		Object struct {
			ID                string `json:"id"`
			Type              string `json:"type"`
			PreferredUsername string `json:"preferredUsername"`
			Inbox             string `json:"inbox"`
			PublicKey         struct {
				ID string `json:"id"`
			} `json:"publicKey"`
		} `json:"object"`
	}
	if err := json.Unmarshal([]byte(due[0].ActivityJSON), &activity); err != nil {
		t.Fatal(err)
	}
	if activity.Type != "Update" || activity.Actor != e.srv.cfg.Actor.ID() {
		t.Fatalf("activity = %+v", activity)
	}
	if activity.Object.ID != e.srv.cfg.Actor.ID() || activity.Object.Type == "" ||
		activity.Object.PreferredUsername != "blog" || activity.Object.Inbox == "" ||
		activity.Object.PublicKey.ID == "" {
		t.Fatalf("object is not a full actor document: %+v", activity.Object)
	}
}

func TestAdminPublishActorReportsStoreFailure(t *testing.T) {
	e := newTestEnv(t)
	if err := e.st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`
		CREATE TRIGGER reject_delivery BEFORE INSERT ON deliveries
		BEGIN SELECT RAISE(ABORT, 'delivery insert refused'); END`); err != nil {
		t.Fatal(err)
	}
	w := adminDo(t, e, http.MethodPost, "https://ap.vrypan.net/admin/actor/publish")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	// Nothing was recorded, so a later retry still publishes.
	stored, err := e.st.GetState(store.ActorFingerprintKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Fatalf("fingerprint = %q, want it rolled back", stored)
	}
}
