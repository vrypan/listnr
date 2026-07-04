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

func (h *Handler) actorDoc() map[string]any {
	id := h.Actor.ID()
	actorType := h.Actor.Type
	if actorType == "" {
		actorType = "Person"
	}
	context := []any{
		"https://www.w3.org/ns/activitystreams",
		"https://w3id.org/security/v1",
	}
	if len(h.Actor.Fields) > 0 {
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
		"preferredUsername": h.Actor.Username,
		"name":              h.Actor.Name,
		"summary":           h.Actor.Summary,
		"url":               h.Actor.BlogURL,
		"inbox":             "https://" + h.Actor.Host + "/inbox",
		"outbox":            "https://" + h.Actor.Host + "/outbox",
		"followers":         "https://" + h.Actor.Host + "/followers",
		"icon": map[string]any{
			"type": "Image",
			"url":  h.Actor.Icon,
		},
		"publicKey": map[string]any{
			"id":           id + "#main-key",
			"owner":        id,
			"publicKeyPem": h.PublicKeyPEM,
		},
	}
	if h.Actor.Header != "" {
		doc["image"] = map[string]any{
			"type": "Image",
			"url":  h.Actor.Header,
		}
	}
	if len(h.Actor.AlsoKnownAs) > 0 {
		doc["alsoKnownAs"] = h.Actor.AlsoKnownAs
	}
	if fields := actorFields(h.Actor.Fields); len(fields) > 0 {
		doc["attachment"] = fields
	}
	if tags := actorTags(h.Actor.Tags); len(tags) > 0 {
		doc["tag"] = tags
	}
	for k, v := range h.Actor.Extra {
		doc[k] = v
	}
	return doc
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
	WriteJSON(w, ContentType, h.actorDoc())
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
