package store

import (
	"testing"
)

func newActorStore(t *testing.T, inboxes ...string) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for i, inbox := range inboxes {
		actorID := "https://remote.example/users/" + string(rune('a'+i))
		if err := st.UpsertFollower(actorID, inbox, ""); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestPublishActorUpdateQueuesOncePerChange(t *testing.T) {
	st := newActorStore(t, "https://a.example/inbox", "https://b.example/inbox")
	activity := []byte(`{"type":"Update"}`)

	first, err := st.PublishActorUpdate("fingerprint-1", activity)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Published || first.Queued != 2 {
		t.Fatalf("first publish = %+v, want published with 2 queued", first)
	}

	// The same profile again must queue nothing.
	second, err := st.PublishActorUpdate("fingerprint-1", activity)
	if err != nil {
		t.Fatal(err)
	}
	if second.Published || second.Queued != 0 {
		t.Fatalf("unchanged publish = %+v, want nothing queued", second)
	}
	if got := countDeliveries(t, st); got != 2 {
		t.Fatalf("delivery rows = %d, want 2", got)
	}

	// A changed profile queues again.
	third, err := st.PublishActorUpdate("fingerprint-2", activity)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Published || third.Queued != 2 {
		t.Fatalf("changed publish = %+v, want published with 2 queued", third)
	}
	if got := countDeliveries(t, st); got != 4 {
		t.Fatalf("delivery rows = %d, want 4", got)
	}
}

func TestPublishActorUpdateRecordsFingerprintWithoutFollowers(t *testing.T) {
	st := newActorStore(t)

	result, err := st.PublishActorUpdate("fingerprint-1", []byte(`{"type":"Update"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Published || result.Queued != 0 {
		t.Fatalf("result = %+v, want published with nothing queued", result)
	}
	stored, err := st.GetState(ActorFingerprintKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "fingerprint-1" {
		t.Fatalf("stored fingerprint = %q, want it recorded even with no audience", stored)
	}

	// Having acknowledged it, a repeat is still a no-op.
	repeat, err := st.PublishActorUpdate("fingerprint-1", []byte(`{"type":"Update"}`))
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Published {
		t.Fatal("repeat publish reported a change")
	}
}

func TestPublishActorUpdateRollsBackOnDeliveryFailure(t *testing.T) {
	st := newActorStore(t, "https://a.example/inbox")
	// A CHECK constraint on status rejects the insert, standing in for any
	// failure partway through the fan-out.
	if _, err := st.DB.Exec(`
		CREATE TRIGGER reject_delivery BEFORE INSERT ON deliveries
		BEGIN SELECT RAISE(ABORT, 'delivery insert refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PublishActorUpdate("fingerprint-1", []byte(`{"type":"Update"}`)); err == nil {
		t.Fatal("want an error when the fan-out fails")
	}
	stored, err := st.GetState(ActorFingerprintKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored != "" {
		t.Fatalf("fingerprint = %q, want it rolled back with the deliveries", stored)
	}
}
