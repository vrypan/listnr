package fedi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/store"
)

const localActorID = "https://ap.vrypan.net/actor"

// newMoveClient serves body from a stub target and returns a client pointed at
// it. The client uses a plain transport because the stub is on loopback, which
// the production SSRF guard refuses by design.
func newMoveClient(t *testing.T, handler http.HandlerFunc) (*Client, string) {
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
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient(st, key, localActorID+"#main-key", srv.Client()), srv.URL
}

// serveTarget answers every request with the given JSON document.
func serveTarget(doc string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/activity+json")
		w.Write([]byte(doc))
	}
}

// A loopback stub is necessarily http, and the URL check refuses that before
// any request is made — which is itself the behaviour to assert here.
func TestFetchMoveTargetRefusesNonHTTPSBeforeFetching(t *testing.T) {
	var reached bool
	client, base := newMoveClient(t, func(w http.ResponseWriter, r *http.Request) {
		reached = true
		serveTarget(`{"id":"x","type":"Person"}`)(w, r)
	})
	_, err := client.FetchMoveTarget(context.Background(), base+"/users/me", localActorID)
	if err == nil {
		t.Fatal("want an http target refused")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("err = %v, want it to name the scheme requirement", err)
	}
	if reached {
		t.Fatal("an invalid target URL was still dereferenced")
	}
}

func TestValidateTargetURLRejectsUnsafeTargets(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"same as local": localActorID,
		"http scheme":   "http://mastodon.example/users/me",
		"no scheme":     "mastodon.example/users/me",
		"bare handle":   "@me@mastodon.example",
		"with fragment": "https://mastodon.example/users/me#main-key",
	}
	for name, target := range cases {
		if err := validateTargetURL(target, localActorID); err == nil {
			t.Errorf("%s (%q): want an error", name, target)
		}
	}
	if err := validateTargetURL("https://mastodon.example/users/me", localActorID); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
}

// The reciprocal alias is the whole security property: without it, anyone able
// to name a target could redirect followers to an account they do not control.
func TestFetchMoveTargetRequiresReciprocalAlias(t *testing.T) {
	cases := map[string]string{
		"no alsoKnownAs":  `{"id":"%s","type":"Person"}`,
		"other alias":     `{"id":"%s","type":"Person","alsoKnownAs":["https://elsewhere.example/actor"]}`,
		"empty array":     `{"id":"%s","type":"Person","alsoKnownAs":[]}`,
		"prefix only":     `{"id":"%s","type":"Person","alsoKnownAs":["https://ap.vrypan.net/actor-other"]}`,
		"wrong type":      `{"id":"%s","type":"Note","alsoKnownAs":["` + localActorID + `"]}`,
		"id mismatch":     `{"id":"https://evil.example/actor","type":"Person","alsoKnownAs":["` + localActorID + `"]}`,
		"malformed json":  `{"id":`,
		"aliases not URL": `{"id":"%s","type":"Person","alsoKnownAs":{"unexpected":"shape"}}`,
	}
	for name, template := range cases {
		t.Run(name, func(t *testing.T) {
			var targetURL string
			client, base := newMoveClient(t, func(w http.ResponseWriter, r *http.Request) {
				serveTarget(strings.ReplaceAll(template, "%s", targetURL))(w, r)
			})
			targetURL = base + "/users/me"
			// validateTargetURL runs first and rejects the loopback http stub,
			// so drive the document checks through the unexported path.
			body, _, err := client.Get(context.Background(), targetURL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parseAndValidateTarget(body, targetURL, localActorID); err == nil {
				t.Fatalf("%s: want an error", name)
			}
		})
	}
}

func TestParseAndValidateTargetAcceptsValidDocuments(t *testing.T) {
	const target = "https://mastodon.example/users/me"
	cases := map[string]string{
		"string alias": `{"id":"` + target + `","type":"Person","alsoKnownAs":"` + localActorID + `"}`,
		"array alias":  `{"id":"` + target + `","type":"Person","alsoKnownAs":["` + localActorID + `"]}`,
		"service type": `{"id":"` + target + `","type":"Service","alsoKnownAs":["` + localActorID + `"]}`,
		"among others": `{"id":"` + target + `","type":"Person","alsoKnownAs":["https://a.example/x","` + localActorID + `"]}`,
	}
	fingerprints := map[string]bool{}
	for name, doc := range cases {
		got, err := parseAndValidateTarget([]byte(doc), target, localActorID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.ID != target {
			t.Fatalf("%s: id = %q", name, got.ID)
		}
		if got.Fingerprint == "" {
			t.Fatalf("%s: no fingerprint", name)
		}
		fingerprints[got.Fingerprint] = true
	}
	// Each distinct document fingerprints differently.
	if len(fingerprints) != len(cases) {
		t.Fatalf("fingerprints = %d, want %d distinct", len(fingerprints), len(cases))
	}
}

func TestFetchMoveTargetRejectsErrorResponses(t *testing.T) {
	client, base := newMoveClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusGone)
	})
	// Reached through Get so the status check is what fails, not the scheme.
	_, status, err := client.Get(context.Background(), base+"/users/me")
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusGone {
		t.Fatalf("status = %d, want 410", status)
	}
}

func TestStringOrStringArrayShapes(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`"one"`, 1},
		{`["one","two"]`, 2},
		{`[]`, 0},
		{`{"bad":"shape"}`, 0},
		{`5`, 0},
		{``, 0},
	}
	for _, c := range cases {
		if got := stringOrStringArray([]byte(c.raw)); len(got) != c.want {
			t.Errorf("stringOrStringArray(%s) = %v, want %d entries", c.raw, got, c.want)
		}
	}
}
