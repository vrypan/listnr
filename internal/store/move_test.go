package store

import (
	"errors"
	"testing"
)

const (
	targetA = "https://mastodon.example/users/vrypan"
	targetB = "https://other.example/users/vrypan"
)

func testMove(target string) Move {
	return Move{
		Target:            target,
		ActivityID:        "https://ap.example/actor#move-abc123",
		TargetFingerprint: "fingerprint-1",
		MovedAt:           "2026-07-20T10:00:00Z",
	}
}

func TestMoveStatusOnAFreshInstance(t *testing.T) {
	st := newActorStore(t)
	outcome, move, err := st.MoveStatus(targetA)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != NotMoved || move != nil {
		t.Fatalf("outcome = %q, move = %+v, want not_moved and nil", outcome, move)
	}
	current, err := st.CurrentMove()
	if err != nil || current != nil {
		t.Fatalf("CurrentMove = %+v (err %v), want nil", current, err)
	}
}

func TestCommitMoveIsIdempotentAndImmutable(t *testing.T) {
	st := newActorStore(t, "https://a.example/inbox", "https://b.example/inbox")
	activity := []byte(`{"type":"Move"}`)

	already, queued, err := st.CommitMove(testMove(targetA), activity)
	if err != nil {
		t.Fatal(err)
	}
	if already || queued != 2 {
		t.Fatalf("first move: already = %v, queued = %d, want false and 2", already, queued)
	}

	stored, err := st.CurrentMove()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Target != targetA || stored.ActivityID == "" ||
		stored.TargetFingerprint != "fingerprint-1" || stored.MovedAt != "2026-07-20T10:00:00Z" {
		t.Fatalf("stored move = %+v", stored)
	}

	// The same target again is a no-op, with no duplicate deliveries.
	same := testMove(targetA)
	same.MovedAt = "2026-07-21T10:00:00Z"
	already, queued, err = st.CommitMove(same, activity)
	if err != nil {
		t.Fatal(err)
	}
	if !already || queued != 0 {
		t.Fatalf("repeat move: already = %v, queued = %d, want true and 0", already, queued)
	}
	if got := countDeliveries(t, st); got != 2 {
		t.Fatalf("delivery rows = %d, want 2", got)
	}
	stored, _ = st.CurrentMove()
	if stored.MovedAt != "2026-07-20T10:00:00Z" {
		t.Fatalf("repeat rewrote moved_at to %q", stored.MovedAt)
	}

	// A different target is refused: followers were already told where to go.
	if _, _, err := st.CommitMove(testMove(targetB), activity); !errors.Is(err, ErrAlreadyMovedElsewhere) {
		t.Fatalf("second target err = %v, want ErrAlreadyMovedElsewhere", err)
	}
	stored, _ = st.CurrentMove()
	if stored.Target != targetA {
		t.Fatalf("target changed to %q", stored.Target)
	}
	if got := countDeliveries(t, st); got != 2 {
		t.Fatalf("delivery rows after refused move = %d, want 2", got)
	}
}

func TestMoveStatusClassifiesTargets(t *testing.T) {
	st := newActorStore(t)
	if _, _, err := st.CommitMove(testMove(targetA), []byte(`{"type":"Move"}`)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		target string
		want   MoveOutcome
	}{
		{targetA, MovedToSameTarget},
		{targetB, MovedToDifferentTarget},
		{"", MovedToSameTarget}, // an empty target queries the state alone
	}
	for _, c := range cases {
		outcome, move, err := st.MoveStatus(c.target)
		if err != nil {
			t.Fatal(err)
		}
		if outcome != c.want {
			t.Fatalf("MoveStatus(%q) = %q, want %q", c.target, outcome, c.want)
		}
		if move == nil || move.Target != targetA {
			t.Fatalf("MoveStatus(%q) move = %+v", c.target, move)
		}
	}
}

func TestCommitMoveRollsBackOnDeliveryFailure(t *testing.T) {
	st := newActorStore(t, "https://a.example/inbox")
	if _, err := st.DB.Exec(`
		CREATE TRIGGER reject_delivery BEFORE INSERT ON deliveries
		BEGIN SELECT RAISE(ABORT, 'delivery insert refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CommitMove(testMove(targetA), []byte(`{"type":"Move"}`)); err == nil {
		t.Fatal("want an error when the fan-out fails")
	}
	// The actor must not be recorded as moved when nobody was told.
	move, err := st.CurrentMove()
	if err != nil {
		t.Fatal(err)
	}
	if move != nil {
		t.Fatalf("move = %+v, want it rolled back with the deliveries", move)
	}
}

func TestCommitMoveWithoutFollowersStillRecordsState(t *testing.T) {
	st := newActorStore(t)
	already, queued, err := st.CommitMove(testMove(targetA), []byte(`{"type":"Move"}`))
	if err != nil {
		t.Fatal(err)
	}
	if already || queued != 0 {
		t.Fatalf("already = %v, queued = %d, want false and 0", already, queued)
	}
	move, err := st.CurrentMove()
	if err != nil || move == nil || move.Target != targetA {
		t.Fatalf("move = %+v (err %v), want the migration recorded", move, err)
	}
}
