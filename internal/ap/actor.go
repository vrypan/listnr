// Package ap renders and serves the ActivityPub documents for the single
// actor listnr manages.
package ap

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vrypan/listnr/internal/config"
)

const ContentType = `application/activity+json; charset=utf-8`

// Handler serves the public ActivityPub endpoints.
type Handler struct {
	Actor        config.Actor
	PublicKeyPEM string
}

// Document builds the actor's complete Person document. It is the single
// source of that document: the HTTP handler serves exactly what the publisher
// fingerprints and sends, so no actor property can be visible in one and
// missing from the other.
//
// The result is deterministic — equal inputs marshal to equal bytes — which is
// what makes fingerprint-based deduplication of actor Updates meaningful.
func Document(actor config.Actor, publicKeyPEM string) map[string]any {
	id := actor.ID()
	actorType := actor.Type
	if actorType == "" {
		actorType = "Person"
	}
	context := []any{
		"https://www.w3.org/ns/activitystreams",
		"https://w3id.org/security/v1",
	}
	if len(actor.Fields) > 0 {
		context = append(context, map[string]any{
			"schema":        "http://schema.org#",
			"PropertyValue": "schema:PropertyValue",
			"value":         "schema:value",
		})
	}
	doc := map[string]any{
		"@context":          context,
		"id":                id,
		"type":              actorType,
		"preferredUsername": actor.Username,
		"name":              actor.Name,
		"summary":           actor.Summary,
		"url":               actor.BlogURL,
		"inbox":             "https://" + actor.Host + "/inbox",
		"outbox":            "https://" + actor.Host + "/outbox",
		"followers":         "https://" + actor.Host + "/followers",
		"icon": map[string]any{
			"type": "Image",
			"url":  actor.Icon,
		},
		"publicKey": map[string]any{
			"id":           id + "#main-key",
			"owner":        id,
			"publicKeyPem": publicKeyPEM,
		},
	}
	if actor.Header != "" {
		doc["image"] = map[string]any{
			"type": "Image",
			"url":  actor.Header,
		}
	}
	if len(actor.AlsoKnownAs) > 0 {
		doc["alsoKnownAs"] = actor.AlsoKnownAs
	}
	if fields := actorFields(actor.Fields); len(fields) > 0 {
		doc["attachment"] = fields
	}
	if tags := actorTags(actor.Tags); len(tags) > 0 {
		doc["tag"] = tags
	}
	for k, v := range actor.Extra {
		doc[k] = v
	}
	return doc
}

// Document returns the actor document this handler serves.
func (h *Handler) Document() map[string]any {
	return Document(h.Actor, h.PublicKeyPEM)
}

func actorFields(fields []config.ActorField) []map[string]any {
	out := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		out = append(out, map[string]any{
			"type":  "PropertyValue",
			"name":  f.Name,
			"value": f.Value,
		})
	}
	return out
}

func actorTags(tags []config.ActorTag) []map[string]any {
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		if t.Name == "" {
			continue
		}
		tag := map[string]any{
			"type": "Hashtag",
			"name": t.Name,
		}
		if t.Href != "" {
			tag["href"] = t.Href
		}
		out = append(out, tag)
	}
	return out
}

func (h *Handler) ServeActor(w http.ResponseWriter, r *http.Request) {
	// Browsers land on the blog; AP servers get the JSON-LD document.
	if !WantsActivityJSON(r) {
		http.Redirect(w, r, h.Actor.BlogURL, http.StatusFound)
		return
	}
	WriteJSON(w, ContentType, h.Document())
}

func WantsActivityJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "activity+json") ||
		strings.Contains(accept, "ld+json") ||
		strings.Contains(accept, "application/json")
}

func WriteJSON(w http.ResponseWriter, contentType string, v any) {
	w.Header().Set("Content-Type", contentType)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
