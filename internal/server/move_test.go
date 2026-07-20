package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/feed"
)

const (
	moveTarget      = "https://mastodon.example/users/vrypan"
	otherMoveTarget = "https://other.example/users/vrypan"
)

// allowMoveTarget makes the fake fetcher treat target as validated: a real
// fetch would have confirmed the reciprocal alias.
func allowMoveTarget(e *testEnv, target string) {
	e.fetcher.targets[target] = &fedi.TargetActor{
		ID: target, Type: "Person",
		AlsoKnownAs: []string{"https://ap.vrypan.net/actor"},
		Fingerprint: "target-fingerprint",
	}
}

func postMove(t *testing.T, e *testEnv, target string) *httptest.ResponseRecorder {
	t.Helper()
	e.srv.cfg.Admin.Token = "secret"
	body, err := json.Marshal(map[string]string{"target": target})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "https://ap.vrypan.net/admin/actor/move", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	return w
}

func TestAdminMoveRequiresAuthorization(t *testing.T) {
	e := newTestEnv(t)
	e.srv.cfg.Admin.Token = "secret"
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		r := httptest.NewRequest(method, "https://ap.vrypan.net/admin/actor/move", nil)
		w := httptest.NewRecorder()
		e.srv.Routes().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s code = %d, want 401", method, w.Code)
		}
	}
}

// A Move is impossible without a target that names this actor back.
func TestAdminMoveRequiresValidatedTarget(t *testing.T) {
	e := newTestEnv(t)
	w := postMove(t, e, moveTarget)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 for an unvalidated target", w.Code)
	}
	if !strings.Contains(w.Body.String(), "alsoKnownAs") {
		t.Fatalf("body = %q, want it to explain the missing reciprocal alias", w.Body.String())
	}
	move, err := e.st.CurrentMove()
	if err != nil {
		t.Fatal(err)
	}
	if move != nil {
		t.Fatalf("move = %+v, want nothing recorded", move)
	}

	// A missing target is a request error, not a validation attempt.
	if w := postMove(t, e, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("empty target code = %d, want 400", w.Code)
	}
}

func TestAdminMoveIsIdempotentAndIrreversible(t *testing.T) {
	e := newTestEnv(t)
	allowMoveTarget(e, moveTarget)
	allowMoveTarget(e, otherMoveTarget)
	if err := e.st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}

	type result struct {
		Target       string `json:"target"`
		ActivityID   string `json:"activity_id"`
		MovedAt      string `json:"moved_at"`
		AlreadyMoved bool   `json:"already_moved"`
		Queued       int    `json:"queued"`
	}

	w := postMove(t, e, moveTarget)
	if w.Code != http.StatusOK {
		t.Fatalf("first move code = %d, body %s", w.Code, w.Body.String())
	}
	var first result
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.AlreadyMoved || first.Queued != 1 || first.Target != moveTarget {
		t.Fatalf("first move = %+v", first)
	}

	// The queued activity is a Move naming both actors.
	due, err := e.st.DueDeliveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("queued deliveries = %d, want 1", len(due))
	}
	var activity struct {
		Type   string   `json:"type"`
		Actor  string   `json:"actor"`
		Object string   `json:"object"`
		Target string   `json:"target"`
		To     []string `json:"to"`
	}
	if err := json.Unmarshal([]byte(due[0].ActivityJSON), &activity); err != nil {
		t.Fatal(err)
	}
	if activity.Type != "Move" || activity.Actor != e.srv.cfg.Actor.ID() ||
		activity.Object != e.srv.cfg.Actor.ID() || activity.Target != moveTarget {
		t.Fatalf("activity = %+v", activity)
	}
	if len(activity.To) != 1 || activity.To[0] != "https://ap.vrypan.net/followers" {
		t.Fatalf("to = %v, want the followers collection", activity.To)
	}

	// The same target again changes nothing and queues nothing.
	w = postMove(t, e, moveTarget)
	if w.Code != http.StatusOK {
		t.Fatalf("repeat move code = %d, want 200", w.Code)
	}
	var second result
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyMoved || second.Queued != 0 || second.MovedAt != first.MovedAt {
		t.Fatalf("repeat move = %+v, want an idempotent no-op", second)
	}
	if due, _ := e.st.DueDeliveries(10); len(due) != 1 {
		t.Fatalf("deliveries after repeat = %d, want 1", len(due))
	}

	// A second, contradictory target is refused.
	w = postMove(t, e, otherMoveTarget)
	if w.Code != http.StatusConflict {
		t.Fatalf("different target code = %d, want 409", w.Code)
	}
	move, _ := e.st.CurrentMove()
	if move.Target != moveTarget {
		t.Fatalf("target changed to %q", move.Target)
	}
}

func TestAdminMoveStatusReportsState(t *testing.T) {
	e := newTestEnv(t)

	w := adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/actor/move")
	var before struct {
		Moved bool `json:"moved"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.Moved {
		t.Fatal("a fresh instance reported itself moved")
	}

	allowMoveTarget(e, moveTarget)
	postMove(t, e, moveTarget)

	w = adminDo(t, e, http.MethodGet, "https://ap.vrypan.net/admin/actor/move")
	var after struct {
		Moved             bool   `json:"moved"`
		Target            string `json:"target"`
		ActivityID        string `json:"activity_id"`
		TargetFingerprint string `json:"target_fingerprint"`
		MovedAt           string `json:"moved_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if !after.Moved || after.Target != moveTarget || after.ActivityID == "" ||
		after.TargetFingerprint != "target-fingerprint" || after.MovedAt == "" {
		t.Fatalf("status = %+v", after)
	}
}

// After a Move the old actor keeps every id and URL it had; only movedTo is
// added. Nothing that already federated may break.
func TestMovedActorRemainsDereferenceable(t *testing.T) {
	e := newTestEnv(t)
	allowMoveTarget(e, moveTarget)
	postMove(t, e, moveTarget)

	w := apGet(t, e, "https://ap.vrypan.net/actor", "")
	if w.Code != http.StatusOK {
		t.Fatalf("actor code = %d, want 200", w.Code)
	}
	var doc struct {
		ID        string `json:"id"`
		MovedTo   string `json:"movedTo"`
		Inbox     string `json:"inbox"`
		Followers string `json:"followers"`
		PublicKey struct {
			ID string `json:"id"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.MovedTo != moveTarget {
		t.Fatalf("movedTo = %q, want %q", doc.MovedTo, moveTarget)
	}
	if doc.ID != "https://ap.vrypan.net/actor" || doc.Inbox != "https://ap.vrypan.net/inbox" ||
		doc.Followers != "https://ap.vrypan.net/followers" ||
		doc.PublicKey.ID != "https://ap.vrypan.net/actor#main-key" {
		t.Fatalf("actor identity changed: %+v", doc)
	}

	// Posts, outbox, followers and webfinger all keep answering.
	for _, target := range []string{
		postAPID,
		"https://ap.vrypan.net/outbox",
		"https://ap.vrypan.net/followers",
	} {
		if got := apGet(t, e, target, "").Code; got != http.StatusOK {
			t.Fatalf("%s code = %d after a move, want 200", target, got)
		}
	}
	r := httptest.NewRequest(http.MethodGet,
		"https://ap.vrypan.net/.well-known/webfinger?resource=acct:blog@vrypan.net", nil)
	w = httptest.NewRecorder()
	e.srv.Routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("webfinger code = %d after a move, want 200", w.Code)
	}
}

// movedTo is part of the actor representation, so it must change the actor's
// validator and its publication fingerprint.
func TestMoveChangesActorETagAndFingerprint(t *testing.T) {
	e := newTestEnv(t)
	before := etagOf(t, e, "https://ap.vrypan.net/actor")
	beforePublish := publishActor(t, e)

	allowMoveTarget(e, moveTarget)
	postMove(t, e, moveTarget)

	if after := etagOf(t, e, "https://ap.vrypan.net/actor"); after == before {
		t.Fatal("actor ETag did not change after a move")
	}
	afterPublish := publishActor(t, e)
	if afterPublish.Fingerprint == beforePublish.Fingerprint {
		t.Fatal("actor fingerprint did not reflect movedTo")
	}
}

// After a Move the old identity accepts no new followers, but the inbox stays
// open so existing ones can still leave.
func TestMovedActorIgnoresNewFollowsButAcceptsUndo(t *testing.T) {
	e := newTestEnv(t)
	// One follower who joined before the move.
	if code := e.post(t, follow("https://remote.example/activities/1")); code != http.StatusAccepted {
		t.Fatalf("pre-move follow code = %d, want 202", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 1 {
		t.Fatalf("followers = %d, want 1", n)
	}
	e.deliver.enqueued = nil

	allowMoveTarget(e, moveTarget)
	postMove(t, e, moveTarget)

	// A new Follow is acknowledged but neither stored nor Accepted.
	if code := e.post(t, follow("https://remote.example/activities/2")); code != http.StatusAccepted {
		t.Fatalf("post-move follow code = %d, want 202", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 1 {
		t.Fatalf("followers = %d after a post-move follow, want 1", n)
	}
	for _, sent := range e.deliver.enqueued {
		if strings.Contains(string(sent.Activity), `"Accept"`) {
			t.Fatal("an Accept was sent after the actor moved")
		}
	}

	// Undo Follow still works, so the existing follower can leave.
	code := e.post(t, map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       "https://remote.example/activities/3",
		"type":     "Undo",
		"actor":    remoteActorID,
		"object":   map[string]any{"type": "Follow", "actor": remoteActorID, "object": "https://ap.vrypan.net/actor"},
	})
	if code != http.StatusAccepted {
		t.Fatalf("undo code = %d, want 202", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 0 {
		t.Fatalf("followers = %d after an undo, want 0", n)
	}
}

func TestManualRefreshReportsTheActorHasMoved(t *testing.T) {
	e := newTestEnv(t)
	e.srv.SetPollFunc(func(ctx context.Context) error {
		return fmt.Errorf("%w (target %s)", feed.ErrMoved, moveTarget)
	})
	w := adminDo(t, e, http.MethodPost, "https://ap.vrypan.net/admin/poll")
	if w.Code != http.StatusConflict {
		t.Fatalf("code = %d, want 409 for a frozen actor", w.Code)
	}
	if !strings.Contains(w.Body.String(), "moved") {
		t.Fatalf("body = %q, want a clear moved result", w.Body.String())
	}
}

func TestRestoreMoveStateSurvivesRestart(t *testing.T) {
	e := newTestEnv(t)
	allowMoveTarget(e, moveTarget)
	postMove(t, e, moveTarget)

	// A fresh handler, as a restarted daemon would build.
	e.srv.ap.SetMovedTo("")
	if err := e.srv.RestoreMoveState(); err != nil {
		t.Fatal(err)
	}
	if got := e.srv.ap.MovedTo(); got != moveTarget {
		t.Fatalf("movedTo after restore = %q, want %q", got, moveTarget)
	}
}
