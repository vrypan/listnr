package server

import (
	"io"
	"net/http"
	"strconv"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/httpcache"
	"github.com/vrypan/listnr/internal/publish"
	"github.com/vrypan/listnr/internal/store"
)

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	apID := "https://" + s.cfg.Actor.Host + "/posts/" + r.PathValue("id")
	post, err := s.st.GetPostByAPID(apID)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if post == nil || !post.APID.Valid {
		http.NotFound(w, r)
		return
	}
	// This URL has two representations, so a shared cache must key on Accept.
	httpcache.AddVary(w, "Accept")
	if post.Deleted() {
		s.servePostGone(w, r, post)
		return
	}
	if !ap.WantsActivityJSON(r) {
		s.serveInterstitial(w, post)
		return
	}
	note := publish.Note(s.cfg.Actor, post)
	// A standalone Note needs its own @context; Mastodon's remote fetch
	// (e.g. via /authorize_interaction) rejects objects without one. In
	// Create/Update fan-out the wrapping activity provides it instead.
	note["@context"] = "https://www.w3.org/ns/activitystreams"
	s.writeAP(w, r, note)
}

// writeAP serves an ActivityPub document with an exact-representation ETag,
// so a repeat fetch of unchanged state costs a bodyless 304.
func (s *Server) writeAP(w http.ResponseWriter, r *http.Request, doc any) {
	if err := httpcache.WriteJSON(w, r, ap.ContentType, ap.CacheControl, doc); err != nil {
		s.log.Error("write activitypub document failed", "path", r.URL.Path, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
	}
}

// servePostGone answers for a withdrawn post. ActivityPub says a server SHOULD
// respond 410 and MAY include a Tombstone, which is also what Mastodon does; a
// browser gets the same status in prose rather than the instance chooser,
// since there is no longer anything to interact with.
func (s *Server) servePostGone(w http.ResponseWriter, r *http.Request, post *store.Post) {
	if !ap.WantsActivityJSON(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		io.WriteString(w, "This post has been deleted.\n")
		return
	}
	// The Tombstone is a real representation, so it is tagged like any other —
	// which also guarantees it does not reuse the Note's validator.
	if err := httpcache.WriteJSONStatus(w, http.StatusGone, ap.ContentType,
		ap.CacheControl, publish.Tombstone(s.cfg.Actor, post)); err != nil {
		s.log.Error("write tombstone failed", "ap_id", post.APID.String, "err", err)
	}
}

func (s *Server) handleOutboxPage(w http.ResponseWriter, r *http.Request, total int) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		http.NotFound(w, r)
		return
	}
	const perPage = 20
	posts, err := s.st.ListFederatedPosts(perPage, (page-1)*perPage)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	items := make([]any, 0, len(posts))
	for i := range posts {
		items = append(items, publish.Create(s.cfg.Actor, &posts[i]))
	}
	id := "https://" + s.cfg.Actor.Host + "/outbox?page=" + strconv.Itoa(page)
	doc := map[string]any{
		"@context":     "https://www.w3.org/ns/activitystreams",
		"id":           id,
		"type":         "OrderedCollectionPage",
		"partOf":       "https://" + s.cfg.Actor.Host + "/outbox",
		"orderedItems": items,
	}
	if page*perPage < total {
		doc["next"] = "https://" + s.cfg.Actor.Host + "/outbox?page=" + strconv.Itoa(page+1)
	}
	s.writeAP(w, r, doc)
}

func (s *Server) handleInteractions(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	payload := map[string]any{
		"post":          url,
		"fediverse_url": nil,
		"likes":         0,
		"boosts":        0,
		"replies":       []any{},
	}
	post, err := s.st.GetPostByURL(url)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	// A withdrawn post reports no counts and no fediverse URL: the widget has
	// nothing left to link to. The stored interactions themselves are kept —
	// they remain useful for moderation and audit.
	if post != nil && !post.Deleted() {
		if post.APID.Valid {
			payload["fediverse_url"] = post.APID.String
		}
		interactions, err := s.st.VisibleInteractionsForPost(post.ID)
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		var replies []any
		likes, boosts := 0, 0
		for _, in := range interactions {
			switch in.Kind {
			case "like":
				likes++
			case "boost":
				boosts++
			case "reply":
				replies = append(replies, map[string]any{
					"author": map[string]any{
						"name":   in.ActorName,
						"handle": in.ActorHandle,
						"url":    in.ActorID,
						"avatar": in.ActorIconURL,
					},
					"content_html": in.ContentHTML,
					"published":    in.Published,
					"url":          in.APID,
					"in_reply_to":  in.InReplyTo,
				})
			}
		}
		payload["likes"] = likes
		payload["boosts"] = boosts
		payload["replies"] = replies
	}

	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "ETag")
	// s-maxage gives Cloudflare a short edge TTL so a burst collapses to ~one
	// origin fetch per window; stale-while-revalidate lets the edge serve the
	// slightly-stale copy instantly while it refreshes in the background, so
	// the origin never sees a spike. max-age=0 keeps browsers revalidating
	// against the ETag (cheap 304s) so the widget stays close to live. See
	// Cloudflare-Cache.md for the Cache Rule and Tiered Cache setup this needs.
	const cacheControl = "public, max-age=0, s-maxage=30, stale-while-revalidate=300"
	if err := httpcache.WriteJSON(w, r, "application/json; charset=utf-8", cacheControl, payload); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
	}
}
