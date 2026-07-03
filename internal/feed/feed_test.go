package feed

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/store"
)

type fakeFanout struct {
	activities [][]byte
}

func (f *fakeFanout) FanOut(activityJSON []byte) error {
	f.activities = append(f.activities, append([]byte(nil), activityJSON...))
	return nil
}

func newFeedTest(t *testing.T, backfill int) (*Poller, *store.Store, *fakeFanout) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	deliver := &fakeFanout{}
	cfg := &config.Config{
		Actor: config.Actor{Username: "blog", Domain: "vrypan.net", Host: "ap.vrypan.net", BlogURL: "https://blog.vrypan.net"},
		Feed:  config.Feed{URL: "https://blog.vrypan.net/index.xml", Backfill: backfill},
	}
	return NewPoller(cfg, st, deliver, slog.New(slog.NewTextHandler(io.Discard, nil))), st, deliver
}

func TestFirstRunBackfillSplit(t *testing.T) {
	p, st, deliver := newFeedTest(t, 1)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	if err := p.ingest(&gofeed.Feed{Items: []*gofeed.Item{
		item("new", "https://blog.vrypan.net/new", "New", now),
		item("old", "https://blog.vrypan.net/old", "Old", now.Add(-time.Hour)),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(deliver.activities) != 0 {
		t.Fatalf("first run fan-out count = %d, want 0", len(deliver.activities))
	}
	var federated, seenOnly int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE ap_id IS NOT NULL`).Scan(&federated); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE ap_id IS NULL`).Scan(&seenOnly); err != nil {
		t.Fatal(err)
	}
	if federated != 1 || seenOnly != 1 {
		t.Fatalf("federated=%d seenOnly=%d, want 1/1", federated, seenOnly)
	}
}

func TestNewAndUpdatedItemsFanOut(t *testing.T) {
	p, _, deliver := newFeedTest(t, 5)
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	if err := p.ingest(&gofeed.Feed{Items: []*gofeed.Item{
		item("guid", "https://blog.vrypan.net/post", "Title", now),
	}}); err != nil {
		t.Fatal(err)
	}
	if len(deliver.activities) != 0 {
		t.Fatalf("first run fan-out count = %d, want 0", len(deliver.activities))
	}
	changed := item("guid", "https://blog.vrypan.net/post", "Title changed", now)
	changed.Description = "changed"
	if err := p.ingest(&gofeed.Feed{Items: []*gofeed.Item{changed}}); err != nil {
		t.Fatal(err)
	}
	if len(deliver.activities) != 1 {
		t.Fatalf("update fan-out count = %d, want 1", len(deliver.activities))
	}
	var act struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(deliver.activities[0], &act); err != nil {
		t.Fatal(err)
	}
	if act.Type != "Update" {
		t.Fatalf("activity type = %q, want Update", act.Type)
	}
}

func item(guid, link, title string, published time.Time) *gofeed.Item {
	return &gofeed.Item{
		GUID:            guid,
		Link:            link,
		Title:           title,
		Description:     "summary",
		PublishedParsed: &published,
	}
}
