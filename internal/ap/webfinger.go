package ap

import "net/http"

// ServeWebfinger answers for both the handle domain (acct:blog@vrypan.net)
// and listnr's own host (acct:blog@ap.vrypan.net) — the latter is required
// by Mastodon's reverse-lookup canonicalization. The subject is always the
// handle-domain form.
func (h *Handler) ServeWebfinger(w http.ResponseWriter, r *http.Request) {
	resource := r.URL.Query().Get("resource")
	canonical := "acct:" + h.Actor.Handle()
	alias := "acct:" + h.Actor.Username + "@" + h.Actor.Host

	if resource != canonical && resource != alias && resource != h.Actor.ID() {
		http.NotFound(w, r)
		return
	}
	WriteJSON(w, "application/jrd+json", map[string]any{
		"subject": canonical,
		"aliases": []string{h.Actor.ID(), h.Actor.BlogURL},
		"links": []map[string]any{
			{
				"rel":  "self",
				"type": "application/activity+json",
				"href": h.Actor.ID(),
			},
			{
				"rel":  "http://webfinger.net/rel/profile-page",
				"type": "text/html",
				"href": h.Actor.BlogURL,
			},
		},
	})
}
