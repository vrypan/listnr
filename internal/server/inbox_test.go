package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

const (
	remoteActorID = "https://remote.example/users/alice"
	remoteKeyID   = remoteActorID + "#main-key"
	postURL       = "https://blog.vrypan.net/2026/07/hello/"
	postAPID      = "https://ap.vrypan.net/posts/abcdef0123456789"
)

type fakeFetcher struct {
	actors map[string]*fedi.Actor
	gone   map[string]bool
}

func (f *fakeFetcher) FetchActor(_ context.Context, id string, _ bool) (*fedi.Actor, error) {
	if i := strings.IndexByte(id, '#'); i >= 0 {
		id = id[:i]
	}
	if f.gone[id] {
		return nil, fmt.Errorf("%s: %w", id, fedi.ErrGone)
	}
	a, ok := f.actors[id]
	if !ok {
		return nil, fmt.Errorf("no such actor %s", id)
	}
	return a, nil
}

type fakeDeliverer struct {
	enqueued []struct {
		Activity []byte
		Inbox    string
	}
}

func (f *fakeDeliverer) Enqueue(a []byte, inbox string) error {
	f.enqueued = append(f.enqueued, struct {
		Activity []byte
		Inbox    string
	}{a, inbox})
	return nil
}

func (f *fakeDeliverer) FanOut(a []byte) error { return f.Enqueue(a, "fanout") }

type testEnv struct {
	srv     *Server
	st      *store.Store
	key     *rsa.PrivateKey // the remote actor's key
	fetcher *fakeFetcher
	deliver *fakeDeliverer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// A federated post interactions can target.
	_, err = st.DB.Exec(`
		INSERT INTO posts (guid, url, title, published_at, ap_id, announced_at)
		VALUES ('guid-1', ?, 'Hello', '2026-07-01T00:00:00Z', ?, '2026-07-01T00:00:00Z')`,
		postURL, postAPID)
	if err != nil {
		t.Fatal(err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM, err := keys.PublicPEM(key)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Actor: config.Actor{
		Username: "blog", Domain: "vrypan.net", Host: "ap.vrypan.net",
		BlogURL: "https://blog.vrypan.net",
	}}
	fetcher := &fakeFetcher{
		actors: map[string]*fedi.Actor{remoteActorID: {
			ID: remoteActorID, PublicKeyPEM: pubPEM, Name: "Alice",
			Handle: "alice@remote.example", IconURL: "https://remote.example/a.png",
			Inbox:       "https://remote.example/users/alice/inbox",
			SharedInbox: "https://remote.example/inbox",
		}},
		gone: map[string]bool{},
	}
	deliver := &fakeDeliverer{}
	log := slog.New(slog.NewTextHandler(nullWriter{}, nil))
	srv := New(cfg, st, &ap.Handler{Actor: cfg.Actor}, fetcher, deliver, log)
	return &testEnv{srv: srv, st: st, key: key, fetcher: fetcher, deliver: deliver}
}

type nullWriter struct{}

func (nullWriter) Write(p []byte) (int, error) { return len(p), nil }

// post delivers a signed activity to the inbox handler and returns the
// response code.
func (e *testEnv) post(t *testing.T, activity map[string]any) int {
	t.Helper()
	body, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	if err := httpsig.Sign(r, body, e.key, remoteKeyID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	return w.Code
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func follow(id string) map[string]any {
	return map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       id,
		"type":     "Follow",
		"actor":    remoteActorID,
		"object":   "https://ap.vrypan.net/actor",
	}
}

func TestFollowAcceptAndUndo(t *testing.T) {
	e := newTestEnv(t)

	if code := e.post(t, follow("https://remote.example/f/1")); code != http.StatusAccepted {
		t.Fatalf("follow: code %d", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers WHERE actor_id=?`, remoteActorID); n != 1 {
		t.Fatalf("followers = %d, want 1", n)
	}
	if len(e.deliver.enqueued) != 1 {
		t.Fatalf("enqueued = %d, want 1 Accept", len(e.deliver.enqueued))
	}
	var accept struct {
		Type   string `json:"type"`
		Actor  string `json:"actor"`
		Object struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"object"`
	}
	if err := json.Unmarshal(e.deliver.enqueued[0].Activity, &accept); err != nil {
		t.Fatal(err)
	}
	if accept.Type != "Accept" || accept.Object.Type != "Follow" ||
		accept.Object.ID != "https://remote.example/f/1" {
		t.Errorf("bad Accept: %+v", accept)
	}
	if e.deliver.enqueued[0].Inbox != "https://remote.example/users/alice/inbox" {
		t.Errorf("Accept sent to %s, want personal inbox", e.deliver.enqueued[0].Inbox)
	}

	// Duplicate follow is idempotent, sends another Accept.
	if code := e.post(t, follow("https://remote.example/f/2")); code != http.StatusAccepted {
		t.Fatalf("re-follow: code %d", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 1 {
		t.Fatalf("followers after re-follow = %d, want 1", n)
	}

	// Undo Follow removes the follower.
	code := e.post(t, map[string]any{
		"id": "https://remote.example/u/1", "type": "Undo", "actor": remoteActorID,
		"object": follow("https://remote.example/f/1"),
	})
	if code != http.StatusAccepted {
		t.Fatalf("undo: code %d", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 0 {
		t.Fatalf("followers after undo = %d, want 0", n)
	}
}

func TestLikeBoostReplyAndUndo(t *testing.T) {
	e := newTestEnv(t)

	like := map[string]any{
		"id": "https://remote.example/l/1", "type": "Like",
		"actor": remoteActorID, "object": postAPID,
	}
	boost := map[string]any{
		"id": "https://remote.example/b/1", "type": "Announce",
		"actor": remoteActorID, "object": postURL, // permalink form also resolves
	}
	reply := map[string]any{
		"id": "https://remote.example/c/1", "type": "Create", "actor": remoteActorID,
		"object": map[string]any{
			"id": "https://remote.example/notes/1", "type": "Note",
			"inReplyTo": postAPID,
			"content":   `<p>Nice post!</p><script>alert(1)</script>`,
			"published": "2026-07-04T10:00:00Z",
		},
	}
	for _, a := range []map[string]any{like, boost, reply} {
		if code := e.post(t, a); code != http.StatusAccepted {
			t.Fatalf("%s: code %d", a["type"], code)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM interactions`); n != 3 {
		t.Fatalf("interactions = %d, want 3", n)
	}

	var content string
	if err := e.st.DB.QueryRow(
		`SELECT content_html FROM interactions WHERE kind='reply'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "<script>") {
		t.Errorf("reply content not sanitized: %q", content)
	}
	if !strings.Contains(content, "Nice post!") {
		t.Errorf("reply content lost: %q", content)
	}

	// Duplicate like ignored.
	e.post(t, like)
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE kind='like'`); n != 1 {
		t.Fatalf("likes after duplicate = %d, want 1", n)
	}

	// Undo Like by inner id.
	e.post(t, map[string]any{
		"id": "https://remote.example/u/2", "type": "Undo", "actor": remoteActorID,
		"object": like,
	})
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE kind='like'`); n != 0 {
		t.Fatalf("likes after undo = %d, want 0", n)
	}

	// Delete the reply note.
	e.post(t, map[string]any{
		"id": "https://remote.example/d/1", "type": "Delete", "actor": remoteActorID,
		"object": "https://remote.example/notes/1",
	})
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE kind='reply'`); n != 0 {
		t.Fatalf("replies after delete = %d, want 0", n)
	}
}

func TestReplyToUnknownPostIgnored(t *testing.T) {
	e := newTestEnv(t)
	code := e.post(t, map[string]any{
		"id": "https://remote.example/c/9", "type": "Create", "actor": remoteActorID,
		"object": map[string]any{
			"id": "https://remote.example/notes/9", "type": "Note",
			"inReplyTo": "https://elsewhere.example/some-post", "content": "hi",
		},
	})
	if code != http.StatusAccepted {
		t.Fatalf("code %d", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM interactions`); n != 0 {
		t.Fatalf("interactions = %d, want 0", n)
	}
}

func TestReplyToStoredReplyUsesOriginalPost(t *testing.T) {
	e := newTestEnv(t)
	first := map[string]any{
		"id": "https://remote.example/c/1", "type": "Create", "actor": remoteActorID,
		"object": map[string]any{
			"id":        "https://remote.example/notes/1",
			"type":      "Note",
			"inReplyTo": postAPID,
			"content":   "first",
		},
	}
	second := map[string]any{
		"id": "https://remote.example/c/2", "type": "Create", "actor": remoteActorID,
		"object": map[string]any{
			"id":        "https://remote.example/notes/2",
			"type":      "Note",
			"inReplyTo": "https://remote.example/notes/1",
			"content":   "nested",
		},
	}
	for _, a := range []map[string]any{first, second} {
		if code := e.post(t, a); code != http.StatusAccepted {
			t.Fatalf("%s: code %d", a["id"], code)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE kind='reply'`); n != 2 {
		t.Fatalf("replies = %d, want 2", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE post_id = (SELECT id FROM posts WHERE ap_id = ?)`, postAPID); n != 2 {
		t.Fatalf("replies on original post = %d, want 2", n)
	}
	// Each reply keeps its raw inReplyTo so the widget can thread them:
	// the nested reply's in_reply_to equals the first reply's ap_id.
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE ap_id='https://remote.example/notes/2' AND in_reply_to='https://remote.example/notes/1'`); n != 1 {
		t.Fatalf("nested reply in_reply_to not stored")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM interactions WHERE ap_id='https://remote.example/notes/1' AND in_reply_to=?`, postAPID); n != 1 {
		t.Fatalf("top-level reply in_reply_to not stored")
	}
}

func TestUnsignedAndBadSignatureRejected(t *testing.T) {
	e := newTestEnv(t)

	body, _ := json.Marshal(follow("https://remote.example/f/1"))
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	w := httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: code %d, want 401", w.Code)
	}

	// Signed with a key that doesn't match the published one.
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	r = httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	if err := httpsig.Sign(r, body, wrongKey, remoteKeyID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: code %d, want 401", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 0 {
		t.Fatalf("followers = %d, want 0", n)
	}
}

func TestActorKeyMismatchRejected(t *testing.T) {
	e := newTestEnv(t)
	// Signed correctly by alice, but the activity claims another actor.
	a := follow("https://remote.example/f/1")
	a["actor"] = "https://remote.example/users/mallory"
	if code := e.post(t, a); code != http.StatusUnauthorized {
		t.Errorf("code %d, want 401", code)
	}
}

func TestBlockedActorDroppedSilently(t *testing.T) {
	e := newTestEnv(t)
	if _, err := e.st.DB.Exec(`INSERT INTO blocks (pattern) VALUES ('remote.example')`); err != nil {
		t.Fatal(err)
	}
	if code := e.post(t, follow("https://remote.example/f/1")); code != http.StatusAccepted {
		t.Errorf("blocked follow: code %d, want 202 (silent drop)", code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 0 {
		t.Fatalf("followers = %d, want 0", n)
	}
}

func TestDeleteForGoneActorPurges(t *testing.T) {
	e := newTestEnv(t)
	if code := e.post(t, follow("https://remote.example/f/1")); code != http.StatusAccepted {
		t.Fatal("setup follow failed")
	}
	e.fetcher.gone[remoteActorID] = true

	body, _ := json.Marshal(map[string]any{
		"id": "https://remote.example/d/9", "type": "Delete",
		"actor": remoteActorID, "object": remoteActorID,
	})
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	key, _ := rsa.GenerateKey(rand.Reader, 2048) // signature is unverifiable anyway
	httpsig.Sign(r, body, key, remoteKeyID)
	w := httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("delete-for-gone-actor: code %d, want 202", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 0 {
		t.Fatalf("followers = %d, want 0 after purge", n)
	}
}

// A Delete signed with a gone key from a *different* host must not be able to
// purge an actor: keyId is attacker-controlled and only needs to 404.
func TestDeleteForGoneActorRejectsForeignKey(t *testing.T) {
	e := newTestEnv(t)
	if code := e.post(t, follow("https://remote.example/f/1")); code != http.StatusAccepted {
		t.Fatal("setup follow failed")
	}
	foreignKey := "https://evil.example/key"
	e.fetcher.gone[foreignKey] = true

	body, _ := json.Marshal(map[string]any{
		"id": "https://evil.example/d/9", "type": "Delete",
		"actor": remoteActorID, "object": remoteActorID,
	})
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	httpsig.Sign(r, body, key, foreignKey)
	w := httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("foreign-key delete: code %d, want 401", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 1 {
		t.Fatalf("followers = %d, want 1 (purge must not happen)", n)
	}
}

// A gone key must not authorize deleting a single interaction (object != actor);
// only an actor deleting itself may pass through the unverifiable path.
func TestDeleteForGoneActorRejectsNonActorObject(t *testing.T) {
	e := newTestEnv(t)
	if code := e.post(t, follow("https://remote.example/f/1")); code != http.StatusAccepted {
		t.Fatal("setup follow failed")
	}
	e.fetcher.gone[remoteActorID] = true

	body, _ := json.Marshal(map[string]any{
		"id": "https://remote.example/d/9", "type": "Delete",
		"actor": remoteActorID, "object": "https://remote.example/notes/1",
	})
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	httpsig.Sign(r, body, key, remoteKeyID)
	w := httptest.NewRecorder()
	e.srv.handleInbox(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("non-actor delete: code %d, want 401", w.Code)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM followers`); n != 1 {
		t.Fatalf("followers = %d, want 1 (purge must not happen)", n)
	}
}

func TestBlockMatching(t *testing.T) {
	cases := []struct {
		pattern, actor string
		want           bool
	}{
		{"https://a.example/users/x", "https://a.example/users/x", true},
		{"https://a.example/users/x", "https://a.example/users/y", false},
		{"spam.example", "https://spam.example/users/x", true},
		{"spam.example", "https://sub.spam.example/users/x", true},
		{"spam.example", "https://notspam.example/users/x", false},
		{"spam.example", "https://spam.example.org/users/x", false},
	}
	for _, c := range cases {
		if got := store.BlockMatches(c.pattern, c.actor); got != c.want {
			t.Errorf("BlockMatches(%q, %q) = %v, want %v", c.pattern, c.actor, got, c.want)
		}
	}
}
