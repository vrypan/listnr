package ap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vrypan/listnr/internal/config"
)

func populatedHandler() *Handler {
	return &Handler{
		Actor: config.Actor{
			Username:    "blog",
			Domain:      "vrypan.net",
			Host:        "ap.vrypan.net",
			Type:        "Service",
			Name:        "Blog",
			Summary:     "A blog on the fediverse",
			BlogURL:     "https://blog.vrypan.net",
			Icon:        "https://blog.vrypan.net/avatar.png",
			Header:      "https://blog.vrypan.net/header.jpg",
			AlsoKnownAs: []string{"https://mastodon.example/@vrypan"},
			Fields:      []config.ActorField{{Name: "Website", Value: "blog.vrypan.net"}},
			Tags:        []config.ActorTag{{Name: "#blogging", Href: "https://mastodon.social/tags/blogging"}},
			Extra:       map[string]any{"discoverable": true},
		},
		PublicKeyPEM: "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n",
	}
}

// The served document and the one a publisher builds must be byte-identical;
// otherwise followers could be told about a profile the actor URL never shows.
func TestServedActorMatchesBuiltDocument(t *testing.T) {
	h := populatedHandler()

	r := httptest.NewRequest(http.MethodGet, "https://ap.vrypan.net/actor", nil)
	r.Header.Set("Accept", "application/activity+json")
	w := httptest.NewRecorder()
	h.ServeActor(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}

	built, err := json.Marshal(Document(h.Actor, h.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	var served, expected any
	if err := json.Unmarshal(w.Body.Bytes(), &served); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(built, &expected); err != nil {
		t.Fatal(err)
	}
	servedJSON, _ := json.Marshal(served)
	expectedJSON, _ := json.Marshal(expected)
	if string(servedJSON) != string(expectedJSON) {
		t.Fatalf("served actor differs from built document:\n%s\n%s", servedJSON, expectedJSON)
	}
}

func TestDocumentSerializationIsDeterministic(t *testing.T) {
	h := populatedHandler()
	first, err := json.Marshal(Document(h.Actor, h.PublicKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := json.Marshal(Document(h.Actor, h.PublicKeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		if string(next) != string(first) {
			t.Fatalf("iteration %d differs:\n%s\n%s", i, first, next)
		}
	}
}

func TestDocumentCarriesPublicKey(t *testing.T) {
	h := populatedHandler()
	key, ok := Document(h.Actor, h.PublicKeyPEM)["publicKey"].(map[string]any)
	if !ok {
		t.Fatal("actor document has no publicKey")
	}
	if key["publicKeyPem"] != h.PublicKeyPEM {
		t.Fatalf("publicKeyPem = %v", key["publicKeyPem"])
	}
	if key["id"] != "https://ap.vrypan.net/actor#main-key" || key["owner"] != "https://ap.vrypan.net/actor" {
		t.Fatalf("publicKey = %#v", key)
	}
}
