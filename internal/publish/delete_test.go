package publish

import (
	"reflect"
	"testing"

	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/store"
)

func deletionFixture() (config.Actor, *store.Post) {
	cfg := config.Actor{
		Username: "blog", Domain: "vrypan.net", Host: "ap.vrypan.net",
		BlogURL: "https://blog.vrypan.net",
	}
	post := &store.Post{
		GUID: "guid-1", URL: "https://blog.vrypan.net/one", Title: "One",
		PublishedAt: "2026-07-01T00:00:00Z",
		APID:        store.NullString("https://ap.vrypan.net/posts/abc123"),
		DeletedAt:   store.NullString("2026-07-20T10:00:00Z"),
	}
	return cfg, post
}

func TestDeleteActivityShape(t *testing.T) {
	cfg, post := deletionFixture()
	want := map[string]any{
		"@context":  "https://www.w3.org/ns/activitystreams",
		"id":        "https://ap.vrypan.net/posts/abc123#delete",
		"type":      "Delete",
		"actor":     "https://ap.vrypan.net/actor",
		"to":        []string{Public},
		"cc":        []string{"https://ap.vrypan.net/followers"},
		"object":    "https://ap.vrypan.net/posts/abc123",
		"published": "2026-07-20T10:00:00Z",
	}
	if got := Delete(cfg, post); !reflect.DeepEqual(got, want) {
		t.Fatalf("Delete() = %#v, want %#v", got, want)
	}
}

func TestTombstoneShape(t *testing.T) {
	cfg, post := deletionFixture()
	want := map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"id":         "https://ap.vrypan.net/posts/abc123",
		"type":       "Tombstone",
		"formerType": "Note",
		"deleted":    "2026-07-20T10:00:00Z",
	}
	if got := Tombstone(cfg, post); !reflect.DeepEqual(got, want) {
		t.Fatalf("Tombstone() = %#v, want %#v", got, want)
	}
}

func TestDeleteIDIsStableAcrossCalls(t *testing.T) {
	cfg, post := deletionFixture()
	first, err := Marshal(Delete(cfg, post))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(Delete(cfg, post))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("Delete() is not deterministic:\n%s\n%s", first, second)
	}
	// An Update for the same post must not collide with its Delete.
	if Delete(cfg, post)["id"] == post.APID.String {
		t.Fatal("Delete id collides with the Note id")
	}
}
