package publish

import (
	"reflect"
	"testing"
)

const moveTarget = "https://mastodon.example/users/vrypan"

func TestMoveActivityShape(t *testing.T) {
	cfg := actorFixture()
	want := map[string]any{
		"@context":  "https://www.w3.org/ns/activitystreams",
		"id":        MoveID(cfg, moveTarget),
		"type":      "Move",
		"actor":     "https://ap.vrypan.net/actor",
		"object":    "https://ap.vrypan.net/actor",
		"target":    moveTarget,
		"to":        []string{"https://ap.vrypan.net/followers"},
		"published": "2026-07-20T10:00:00Z",
	}
	got := Move(cfg, moveTarget, "2026-07-20T10:00:00Z")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Move() = %#v, want %#v", got, want)
	}
	// Migration is addressed to followers, not broadcast publicly.
	if to := got["to"].([]string); len(to) != 1 || to[0] == Public {
		t.Fatalf("to = %v, want only the followers collection", to)
	}
}

func TestMoveIDIsStableAndTargetSpecific(t *testing.T) {
	cfg := actorFixture()
	first := MoveID(cfg, moveTarget)
	if first != MoveID(cfg, moveTarget) {
		t.Fatal("MoveID is not stable for the same target")
	}
	if first == MoveID(cfg, "https://other.example/users/vrypan") {
		t.Fatal("different targets produced the same move id")
	}
	// The id belongs to the old actor, so a receiver can attribute it.
	if got := MoveID(cfg, moveTarget); got[:len(cfg.ID())] != cfg.ID() {
		t.Fatalf("MoveID = %q, want it prefixed with the old actor id", got)
	}
}

func TestMoveActivityIsDeterministic(t *testing.T) {
	cfg := actorFixture()
	first, err := Marshal(Move(cfg, moveTarget, "2026-07-20T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(Move(cfg, moveTarget, "2026-07-20T10:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("Move() is not deterministic:\n%s\n%s", first, second)
	}
}
