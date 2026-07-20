package publish

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
)

func actorFixture() config.Actor {
	return config.Actor{
		Username: "blog", Domain: "vrypan.net", Host: "ap.vrypan.net",
		Name: "Blog", Summary: "A blog", BlogURL: "https://blog.vrypan.net",
		Icon: "https://blog.vrypan.net/avatar.png",
	}
}

const testPublicKeyPEM = "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n"

func TestActorUpdateCarriesFullActorAndAudience(t *testing.T) {
	cfg := actorFixture()
	doc := ap.Document(cfg, testPublicKeyPEM, "")
	activity, fingerprint, err := ActorUpdate(cfg, doc)
	if err != nil {
		t.Fatal(err)
	}
	if activity["type"] != "Update" || activity["actor"] != cfg.ID() {
		t.Fatalf("activity = %#v", activity)
	}
	if !reflect.DeepEqual(activity["to"], []string{Public}) {
		t.Fatalf("to = %#v, want Public", activity["to"])
	}
	if !reflect.DeepEqual(activity["cc"], []string{"https://ap.vrypan.net/followers"}) {
		t.Fatalf("cc = %#v, want the followers collection", activity["cc"])
	}
	// The object must be the whole actor, not a patch.
	object, ok := activity["object"].(map[string]any)
	if !ok {
		t.Fatalf("object = %#v, want the actor document", activity["object"])
	}
	if !reflect.DeepEqual(object, doc) {
		t.Fatal("object is not the full actor document")
	}
	if got := activity["id"]; got != cfg.ID()+"#update-"+fingerprint {
		t.Fatalf("id = %v, want it derived from the fingerprint", got)
	}
}

func TestActorFingerprintTracksEveryVisibleChange(t *testing.T) {
	cfg := actorFixture()
	baseline, _, err := fingerprintOf(cfg, testPublicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}

	// Rebuilding the same profile must not look like a change.
	repeat, _, err := fingerprintOf(cfg, testPublicKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != baseline {
		t.Fatal("unchanged profile produced a different fingerprint")
	}

	changes := map[string]func(*config.Actor){
		"name":         func(a *config.Actor) { a.Name = "Renamed" },
		"summary":      func(a *config.Actor) { a.Summary = "Different bio" },
		"icon":         func(a *config.Actor) { a.Icon = "https://blog.vrypan.net/new.png" },
		"header":       func(a *config.Actor) { a.Header = "https://blog.vrypan.net/header.jpg" },
		"fields":       func(a *config.Actor) { a.Fields = []config.ActorField{{Name: "Site", Value: "x"}} },
		"tags":         func(a *config.Actor) { a.Tags = []config.ActorTag{{Name: "#go"}} },
		"also_known_as": func(a *config.Actor) {
			a.AlsoKnownAs = []string{"https://mastodon.example/@vrypan"}
		},
		"extra": func(a *config.Actor) { a.Extra = map[string]any{"discoverable": true} },
	}
	for name, mutate := range changes {
		changed := actorFixture()
		mutate(&changed)
		got, id, err := fingerprintOf(changed, testPublicKeyPEM)
		if err != nil {
			t.Fatal(err)
		}
		if got == baseline {
			t.Fatalf("changing %s did not change the fingerprint", name)
		}
		if !strings.HasSuffix(id, got) {
			t.Fatalf("changing %s left the activity id stale: %s", name, id)
		}
	}

	// The public key is part of the profile too.
	rotated, _, err := fingerprintOf(cfg, "-----BEGIN PUBLIC KEY-----\nOTHER\n-----END PUBLIC KEY-----\n")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == baseline {
		t.Fatal("a different public key did not change the fingerprint")
	}
}

func fingerprintOf(cfg config.Actor, publicKeyPEM string) (fingerprint, activityID string, err error) {
	activity, fingerprint, err := ActorUpdate(cfg, ap.Document(cfg, publicKeyPEM, ""))
	if err != nil {
		return "", "", err
	}
	return fingerprint, activity["id"].(string), nil
}
