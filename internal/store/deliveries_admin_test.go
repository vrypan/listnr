package store

import (
	"errors"
	"testing"
)

// seedDelivery inserts a delivery row directly so a test can choose its
// status, which the queue would otherwise only reach through real attempts.
func seedDelivery(t *testing.T, st *Store, activityJSON, inbox, status string, attempts int) int64 {
	t.Helper()
	res, err := st.DB.Exec(`
		INSERT INTO deliveries (activity_json, inbox_url, status, attempts, next_attempt_at, last_error)
		VALUES (?, ?, ?, ?, '2026-07-20T10:00:00Z', ?)`,
		activityJSON, inbox, status, attempts, "boom")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func newDeliveryStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestListDeliveriesFiltersOrdersAndPaginates(t *testing.T) {
	st := newDeliveryStore(t)
	seedDelivery(t, st, `{"id":"https://ap.example/posts/1#create","type":"Create"}`, "https://a.example/inbox", "pending", 0)
	seedDelivery(t, st, `{"id":"https://ap.example/posts/2#delete","type":"Delete"}`, "https://b.example/inbox", "failed", 7)
	seedDelivery(t, st, `{"id":"https://ap.example/actor#update-abc","type":"Update"}`, "https://c.example/inbox", "done", 1)

	all, err := st.ListDeliveries("", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all deliveries = %d, want 3", len(all))
	}
	// Newest first: ids descend.
	if all[0].ID <= all[1].ID || all[1].ID <= all[2].ID {
		t.Fatalf("ordering = %d, %d, %d, want descending", all[0].ID, all[1].ID, all[2].ID)
	}

	failed, err := st.ListDeliveries("failed", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].Status != "failed" {
		t.Fatalf("failed filter = %+v", failed)
	}
	if failed[0].ActivityType != "Delete" || failed[0].ActivityID != "https://ap.example/posts/2#delete" {
		t.Fatalf("activity metadata = %+v", failed[0])
	}
	if failed[0].Attempts != 7 || failed[0].LastError != "boom" {
		t.Fatalf("failed row = %+v", failed[0])
	}

	page, err := st.ListDeliveries("", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ID != all[1].ID {
		t.Fatalf("page = %+v, want the second row", page)
	}

	empty, err := st.ListDeliveries("pending", 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("past-the-end page = %+v, want an empty non-nil slice", empty)
	}

	if _, err := st.ListDeliveries("bogus", 100, 0); err == nil {
		t.Fatal("want an error for an unknown status filter")
	}
}

func TestListDeliveriesToleratesMalformedActivityJSON(t *testing.T) {
	st := newDeliveryStore(t)
	seedDelivery(t, st, `not json at all`, "https://a.example/inbox", "failed", 1)
	rows, err := st.ListDeliveries("", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the corrupt row to remain listable", len(rows))
	}
	if rows[0].ActivityType != "" || rows[0].ActivityID != "" {
		t.Fatalf("metadata = %+v, want empty for an unparseable payload", rows[0])
	}
}

func TestRetryDeliveryOnlyAcceptsFailedRows(t *testing.T) {
	st := newDeliveryStore(t)
	failed := seedDelivery(t, st, `{"type":"Create"}`, "https://a.example/inbox", "failed", 7)
	pending := seedDelivery(t, st, `{"type":"Create"}`, "https://b.example/inbox", "pending", 2)
	done := seedDelivery(t, st, `{"type":"Create"}`, "https://c.example/inbox", "done", 1)

	if err := st.RetryDelivery(failed); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListDeliveries("", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID != failed {
			continue
		}
		if r.Status != "pending" || r.Attempts != 0 || r.LastError != "" {
			t.Fatalf("retried row = %+v, want pending with a fresh retry budget", r)
		}
	}

	// The worker may already be sending a pending row, and a done row has
	// nothing to retry.
	if err := st.RetryDelivery(pending); !errors.Is(err, ErrDeliveryState) {
		t.Fatalf("retry pending err = %v, want ErrDeliveryState", err)
	}
	if err := st.RetryDelivery(done); !errors.Is(err, ErrDeliveryState) {
		t.Fatalf("retry done err = %v, want ErrDeliveryState", err)
	}
	if err := st.RetryDelivery(9999); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("retry unknown err = %v, want ErrDeliveryNotFound", err)
	}
}

func TestRetryFailedDeliveriesCountsExactly(t *testing.T) {
	st := newDeliveryStore(t)
	seedDelivery(t, st, `{"type":"Create"}`, "https://a.example/inbox", "failed", 7)
	seedDelivery(t, st, `{"type":"Create"}`, "https://b.example/inbox", "failed", 7)
	seedDelivery(t, st, `{"type":"Create"}`, "https://c.example/inbox", "pending", 0)
	seedDelivery(t, st, `{"type":"Create"}`, "https://d.example/inbox", "done", 1)

	n, err := st.RetryFailedDeliveries()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("retried = %d, want 2", n)
	}
	remaining, err := st.ListDeliveries("failed", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("failed rows left = %d, want 0", len(remaining))
	}
	// Nothing else moved.
	done, err := st.ListDeliveries("done", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 {
		t.Fatalf("done rows = %d, want 1 untouched", len(done))
	}

	// With nothing failed, a second sweep reports zero rather than erroring.
	if n, err := st.RetryFailedDeliveries(); err != nil || n != 0 {
		t.Fatalf("second sweep = %d (err %v), want 0", n, err)
	}
}

func TestDeleteDeliveryOnlyAcceptsTerminalRows(t *testing.T) {
	st := newDeliveryStore(t)
	failed := seedDelivery(t, st, `{"type":"Create"}`, "https://a.example/inbox", "failed", 7)
	done := seedDelivery(t, st, `{"type":"Create"}`, "https://b.example/inbox", "done", 1)
	pending := seedDelivery(t, st, `{"type":"Create"}`, "https://c.example/inbox", "pending", 0)

	if err := st.DeleteDelivery(failed); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDelivery(done); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDelivery(pending); !errors.Is(err, ErrDeliveryState) {
		t.Fatalf("delete pending err = %v, want ErrDeliveryState", err)
	}
	if err := st.DeleteDelivery(9999); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("delete unknown err = %v, want ErrDeliveryNotFound", err)
	}
	rows, err := st.ListDeliveries("", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != pending {
		t.Fatalf("remaining rows = %+v, want only the pending one", rows)
	}
}
