package ap

import (
	"testing"

	"github.com/vrypan/listnr/internal/config"
)

func TestActorDocOptionalProfileProperties(t *testing.T) {
	h := &Handler{Actor: config.Actor{
		Username:    "blog",
		Domain:      "vrypan.net",
		Host:        "ap.vrypan.net",
		Name:        "Blog",
		BlogURL:     "https://blog.vrypan.net",
		Icon:        "https://blog.vrypan.net/avatar.png",
		Header:      "https://blog.vrypan.net/header.jpg",
		AlsoKnownAs: []string{"https://mastodon.example/@vrypan"},
		Fields: []config.ActorField{
			{Name: "Website", Value: `<a href="https://blog.vrypan.net" rel="me">blog.vrypan.net</a>`},
			{Name: "Empty", Value: ""},
		},
		Tags: []config.ActorTag{
			{Name: "#blogging", Href: "https://mastodon.social/tags/blogging"},
			{Name: ""},
		},
	}}
	doc := h.actorDoc()
	if _, ok := doc["image"]; !ok {
		t.Fatal("actor doc missing image")
	}
	if got := doc["alsoKnownAs"].([]string)[0]; got != "https://mastodon.example/@vrypan" {
		t.Fatalf("alsoKnownAs = %q", got)
	}
	fields := doc["attachment"].([]map[string]any)
	if len(fields) != 1 || fields[0]["type"] != "PropertyValue" || fields[0]["name"] != "Website" {
		t.Fatalf("bad fields: %#v", fields)
	}
	tags := doc["tag"].([]map[string]any)
	if len(tags) != 1 || tags[0]["type"] != "Hashtag" || tags[0]["name"] != "#blogging" {
		t.Fatalf("bad tags: %#v", tags)
	}
}
