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
	return map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			"https://w3id.org/security/v1",
		},
		"id":                id,
		"type":              "Person",
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
}

func (h *Handler) ServeActor(w http.ResponseWriter, r *http.Request) {
	// Browsers land on the blog; AP servers get the JSON-LD document.
	if !wantsActivityJSON(r) {
		http.Redirect(w, r, h.Actor.BlogURL, http.StatusFound)
		return
	}
	WriteJSON(w, ContentType, h.actorDoc())
}

func wantsActivityJSON(r *http.Request) bool {
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
