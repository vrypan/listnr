package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/delivery"
	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

// TestEndToEndFollow exercises the real stack over HTTP: a fake remote
// instance serves its actor document (requiring a signed GET), sends a
// signed Follow, and receives the signed Accept via the real delivery queue.
func TestEndToEndFollow(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	remoteKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	remotePubPEM, _ := keys.PublicPEM(remoteKey)

	var acceptCount atomic.Int32
	var remoteURL string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/alice":
			// Authorized-fetch simulation: require our GET to be signed.
			if r.Header.Get("Signature") == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/activity+json")
			json.NewEncoder(w).Encode(map[string]any{
				"id":                remoteURL + "/users/alice",
				"type":              "Person",
				"preferredUsername": "alice",
				"name":              "Alice",
				"inbox":             remoteURL + "/users/alice/inbox",
				"endpoints":         map[string]any{"sharedInbox": remoteURL + "/inbox"},
				"publicKey": map[string]any{
					"id":           remoteURL + "/users/alice#main-key",
					"owner":        remoteURL + "/users/alice",
					"publicKeyPem": remotePubPEM,
				},
			})
		case "/users/alice/inbox":
			body, _ := io.ReadAll(r.Body)
			var act struct {
				Type string `json:"type"`
			}
			json.Unmarshal(body, &act)
			if act.Type == "Accept" && r.Header.Get("Signature") != "" {
				acceptCount.Add(1)
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	remoteURL = remote.URL
	actorID := remoteURL + "/users/alice"

	cfg := &config.Config{Actor: config.Actor{
		Username: "blog", Domain: "vrypan.net", Host: "ap.vrypan.net",
		BlogURL: "https://blog.vrypan.net",
	}}
	ourKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	keyID := cfg.Actor.ID() + "#main-key"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := fedi.NewClient(st, ourKey, keyID)
	queue := delivery.NewQueue(st, ourKey, keyID, log)
	srv := New(cfg, st, &ap.Handler{Actor: cfg.Actor}, client, queue, log)

	body, _ := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       remoteURL + "/f/1",
		"type":     "Follow",
		"actor":    actorID,
		"object":   "https://ap.vrypan.net/actor",
	})
	r := httptest.NewRequest("POST", "https://ap.vrypan.net/inbox", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/activity+json")
	if err := httpsig.Sign(r, body, remoteKey, actorID+"#main-key"); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	srv.handleInbox(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("follow: code %d, body %s", w.Code, w.Body.String())
	}

	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM followers WHERE actor_id=?`, actorID).Scan(&n)
	if n != 1 {
		t.Fatalf("followers = %d, want 1", n)
	}
	// Actor must be cached now.
	cached, err := st.GetCachedActor(actorID)
	if err != nil || cached == nil || cached.Handle == "" {
		t.Fatalf("actor not cached: %v %+v", err, cached)
	}

	queue.ProcessDue(context.Background())
	if acceptCount.Load() != 1 {
		t.Fatalf("remote received %d Accepts, want 1", acceptCount.Load())
	}
}
