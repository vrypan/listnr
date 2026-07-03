package delivery

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

func newQueue(t *testing.T) (*Queue, *store.Store, *rsa.PrivateKey) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	log := slogDiscard()
	return NewQueue(st, key, "https://ap.vrypan.net/actor#main-key", log), st, key
}

func TestDeliverySignedAndMarkedDone(t *testing.T) {
	q, st, key := newQueue(t)
	pubPEM, _ := keys.PublicPEM(key)
	pub, err := keys.ParsePublicPEM(pubPEM)
	if err != nil {
		t.Fatal(err)
	}

	var got atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig, err := httpsig.ParseSignature(r.Header.Get("Signature"))
		if err != nil {
			t.Errorf("unsigned delivery: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := httpsig.Verify(r, body, pub, sig); err != nil {
			t.Errorf("delivery signature invalid: %v", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/activity+json" {
			t.Errorf("content-type = %q", ct)
		}
		got.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer remote.Close()

	if err := q.Enqueue([]byte(`{"type":"Accept"}`), remote.URL+"/inbox"); err != nil {
		t.Fatal(err)
	}
	q.ProcessDue(context.Background())

	if got.Load() != 1 {
		t.Fatalf("remote received %d deliveries, want 1", got.Load())
	}
	var status string
	if err := st.DB.QueryRow(`SELECT status FROM deliveries`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "done" {
		t.Errorf("status = %q, want done", status)
	}
}

func TestDeliveryRetriesThenBacksOff(t *testing.T) {
	q, st, _ := newQueue(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer remote.Close()

	q.Enqueue([]byte(`{}`), remote.URL+"/inbox")
	q.ProcessDue(context.Background())

	var status, nextAt string
	var attempts int
	err := st.DB.QueryRow(`SELECT status, attempts, next_attempt_at FROM deliveries`).
		Scan(&status, &attempts, &nextAt)
	if err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 {
		t.Errorf("status=%q attempts=%d, want pending/1", status, attempts)
	}
	next, _ := time.Parse(time.RFC3339, nextAt)
	if until := time.Until(next); until < 30*time.Second || until > 2*time.Minute {
		t.Errorf("next attempt in %s, want ~1m", until)
	}
	// Not due anymore: another pass must not attempt it.
	q.ProcessDue(context.Background())
	st.DB.QueryRow(`SELECT attempts FROM deliveries`).Scan(&attempts)
	if attempts != 1 {
		t.Errorf("attempts after early pass = %d, want 1", attempts)
	}
}

func TestGoneInboxDropsFollowers(t *testing.T) {
	q, st, _ := newQueue(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer remote.Close()
	inbox := remote.URL + "/inbox"

	if err := st.UpsertFollower("https://gone.example/u/1", inbox, ""); err != nil {
		t.Fatal(err)
	}
	q.Enqueue([]byte(`{}`), inbox)
	q.ProcessDue(context.Background())

	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM followers`).Scan(&n)
	if n != 0 {
		t.Errorf("followers = %d, want 0 after 410", n)
	}
	var status string
	st.DB.QueryRow(`SELECT status FROM deliveries`).Scan(&status)
	if status != "failed" {
		t.Errorf("status = %q, want failed", status)
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		time.Minute, 5 * time.Minute, 30 * time.Minute,
		2 * time.Hour, 6 * time.Hour, 24 * time.Hour, 48 * time.Hour,
	}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
	if Backoff(0) != time.Minute || Backoff(100) != 48*time.Hour {
		t.Error("Backoff clamping wrong")
	}
}

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
