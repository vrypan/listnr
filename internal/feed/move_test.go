package feed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/store"
)

// After a Move the old identity must stop ingesting and publishing. Nothing is
// fetched at all, so a broken or changed feed cannot resurrect it.
func TestPollIsFrozenAfterAMove(t *testing.T) {
	p, st, deliver := newFeedTest(t, 10)

	var fetched int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched++
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<rss version="2.0"><channel><title>Blog</title>
			<item><title>Fresh</title><link>https://blog.vrypan.net/fresh</link>
			<guid>fresh</guid><pubDate>Mon, 20 Jul 2026 10:00:00 GMT</pubDate></item>
			</channel></rss>`))
	}))
	defer srv.Close()
	p.cfg.Feed.URL = srv.URL
	p.http = srv.Client()

	// Before the move, polling works normally.
	if err := p.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fetched != 1 {
		t.Fatalf("fetches = %d, want 1 before the move", fetched)
	}
	postsBefore, err := st.TotalPostCount()
	if err != nil {
		t.Fatal(err)
	}
	deliveriesBefore := len(deliver.activities)

	if _, _, err := st.CommitMove(store.Move{
		Target:     "https://mastodon.example/users/vrypan",
		ActivityID: "https://ap.vrypan.net/actor#move-abc",
		MovedAt:    "2026-07-20T10:00:00Z",
	}, []byte(`{"type":"Move"}`)); err != nil {
		t.Fatal(err)
	}

	err = p.Poll(context.Background())
	if !errors.Is(err, ErrMoved) {
		t.Fatalf("Poll after a move = %v, want ErrMoved", err)
	}
	if err == nil || !strings.Contains(err.Error(), "mastodon.example") {
		t.Fatalf("err = %v, want it to name the target", err)
	}
	if fetched != 1 {
		t.Fatalf("fetches = %d, want no further fetch after the move", fetched)
	}

	// Nothing new was ingested and nothing was published.
	postsAfter, err := st.TotalPostCount()
	if err != nil {
		t.Fatal(err)
	}
	if postsAfter != postsBefore {
		t.Fatalf("posts = %d, want %d (no ingestion after a move)", postsAfter, postsBefore)
	}
	if len(deliver.activities) != deliveriesBefore {
		t.Fatalf("published %d activities after a move, want none",
			len(deliver.activities)-deliveriesBefore)
	}
}

// Queued pre-Move deliveries and existing posts are preserved: freezing means
// "publish nothing new", not "discard what was already sent".
func TestMoveFreezePreservesExistingWork(t *testing.T) {
	p, st, _ := newFeedTest(t, 10)
	if _, err := st.InsertPost(&store.Post{
		GUID: "guid-1", URL: "https://blog.vrypan.net/one", Title: "One",
		PublishedAt: "2026-07-01T00:00:00Z",
		APID:        store.NullString("https://ap.vrypan.net/posts/one"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFollower("https://remote.example/users/alice",
		"https://remote.example/users/alice/inbox", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueDelivery(`{"type":"Create"}`, "https://remote.example/users/alice/inbox"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := st.CommitMove(store.Move{
		Target:  "https://mastodon.example/users/vrypan",
		MovedAt: "2026-07-20T10:00:00Z",
	}, []byte(`{"type":"Move"}`)); err != nil {
		t.Fatal(err)
	}
	if err := p.Poll(context.Background()); !errors.Is(err, ErrMoved) {
		t.Fatalf("Poll = %v, want ErrMoved", err)
	}

	// The pre-Move Create is still queued alongside the Move itself.
	due, err := st.DueDeliveries(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 2 {
		t.Fatalf("queued deliveries = %d, want the pre-move Create and the Move", len(due))
	}
	if n, err := st.PostCount(); err != nil || n != 1 {
		t.Fatalf("posts = %d (err %v), want the existing post preserved", n, err)
	}
}
