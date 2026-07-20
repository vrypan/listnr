package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vrypan/listnr/internal/ap"
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
	ap.WriteJSON(w, ap.ContentType, note)
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
	w.Header().Set("Content-Type", ap.ContentType)
	w.WriteHeader(http.StatusGone)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(publish.Tombstone(s.cfg.Actor, post))
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
	ap.WriteJSON(w, ap.ContentType, doc)
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

	// Serialize once (matching WriteJSON's non-HTML-escaping) so the ETag is
	// a fingerprint of the exact bytes we'd send.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", "ETag")
	// s-maxage gives Cloudflare a short edge TTL so a burst collapses to ~one
	// origin fetch per window; stale-while-revalidate lets the edge serve the
	// slightly-stale copy instantly while it refreshes in the background, so
	// the origin never sees a spike. max-age=0 keeps browsers revalidating
	// against the ETag (cheap 304s) so the widget stays close to live. See
	// Cloudflare-Cache.md for the Cache Rule and Tiered Cache setup this needs.
	h.Set("Cache-Control", "public, max-age=0, s-maxage=30, stale-while-revalidate=300")
	h.Set("ETag", etag)
	h.Set("Content-Type", "application/json; charset=utf-8")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(body)
}

// etagMatches reports whether an If-None-Match header covers etag, handling
// the "*", comma-separated, and weak-validator ("W/") forms.
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	for _, tok := range strings.Split(ifNoneMatch, ",") {
		tok = strings.TrimSpace(tok)
		tok = strings.TrimPrefix(tok, "W/")
		if tok == "*" || tok == etag {
			return true
		}
	}
	return false
}
