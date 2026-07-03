package server

import (
	"net/http"
	"strconv"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/publish"
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
	if !ap.WantsActivityJSON(r) {
		http.Redirect(w, r, post.URL, http.StatusFound)
		return
	}
	ap.WriteJSON(w, ap.ContentType, publish.Note(s.cfg.Actor, post))
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")
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
	if post == nil {
		ap.WriteJSON(w, "application/json; charset=utf-8", payload)
		return
	}
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
			})
		}
	}
	payload["likes"] = likes
	payload["boosts"] = boosts
	payload["replies"] = replies
	ap.WriteJSON(w, "application/json; charset=utf-8", payload)
}
