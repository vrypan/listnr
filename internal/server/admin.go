package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vrypan/listnr/internal/backup"
	"github.com/vrypan/listnr/internal/buildinfo"
	"github.com/vrypan/listnr/internal/publish"
	"github.com/vrypan/listnr/internal/store"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	if !s.adminAuthorized(r) {
		if s.cfg.Admin.Token == "" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "replies":
		s.adminReplies(w, r)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "replies" && parts[2] == "hide":
		s.adminToggleReply(w, parts[1], true)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "replies" && parts[2] == "unhide":
		s.adminToggleReply(w, parts[1], false)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "replies":
		s.adminDeleteReply(w, parts[1])
	case r.Method == http.MethodGet && path == "blocks":
		s.adminBlocks(w)
	case r.Method == http.MethodPost && path == "blocks":
		s.adminAddBlock(w, r)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "blocks":
		s.adminDeleteBlock(w, parts[1])
	case r.Method == http.MethodDelete && path == "blocks":
		var body struct {
			Pattern string `json:"pattern"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Pattern == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.adminDeleteBlock(w, body.Pattern)
	case r.Method == http.MethodGet && path == "followers":
		s.adminFollowers(w)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "followers":
		s.adminDeleteFollower(w, parts[1])
	case r.Method == http.MethodGet && path == "posts":
		s.adminPosts(w, r)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "posts":
		s.adminDeletePost(w, parts[1])
	case r.Method == http.MethodGet && path == "deliveries":
		s.adminDeliveries(w, r)
	case r.Method == http.MethodPost && path == "deliveries/retry-failed":
		s.adminRetryFailedDeliveries(w)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "deliveries" && parts[2] == "retry":
		s.adminDeliveryAction(w, parts[1], s.st.RetryDelivery)
	case r.Method == http.MethodDelete && len(parts) == 2 && parts[0] == "deliveries":
		s.adminDeliveryAction(w, parts[1], s.st.DeleteDelivery)
	case r.Method == http.MethodPost && path == "actor/publish":
		s.adminPublishActor(w)
	case r.Method == http.MethodGet && path == "stats":
		s.adminStats(w)
	case r.Method == http.MethodPost && path == "poll":
		s.adminPoll(w, r)
	case r.Method == http.MethodPost && path == "export":
		s.adminExport(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) adminExport(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		http.Error(w, "export unavailable", http.StatusServiceUnavailable)
		return
	}
	f, err := os.CreateTemp("", "listnr-export-*.tar.gz")
	if err != nil {
		s.log.Error("create export file", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	path := f.Name()
	defer os.Remove(path)
	defer f.Close()
	manifest, err := backup.Write(r.Context(), f, backup.Source{
		Store:      s.st,
		DataDir:    s.cfg.Server.DataDir,
		ConfigPath: s.configPath,
		Actor:      s.cfg.Actor,
	})
	if err != nil {
		s.log.Error("create instance export", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.log.Error("seek instance export", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	info, err := f.Stat()
	if err != nil {
		s.log.Error("stat instance export", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	name := "listnr-backup-" + strings.ReplaceAll(manifest.CreatedAt, ":", "") + ".tar.gz"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) adminAuthorized(r *http.Request) bool {
	if s.cfg.Admin.Token == "" {
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	gh := sha256.Sum256([]byte(got))
	wh := sha256.Sum256([]byte(s.cfg.Admin.Token))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}

func (s *Server) adminReplies(w http.ResponseWriter, r *http.Request) {
	replies, err := s.st.ListReplies(r.URL.Query().Get("post"), r.URL.Query().Get("hidden") != "")
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, replies)
}

func (s *Server) adminToggleReply(w http.ResponseWriter, rawID string, hidden bool) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if err := s.st.HideInteraction(id, hidden); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

func (s *Server) adminDeleteReply(w http.ResponseWriter, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if err := s.st.DeleteInteractionByID(id); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

func (s *Server) adminBlocks(w http.ResponseWriter) {
	blocks, err := s.st.ListBlocks()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, blocks)
}

func (s *Server) adminAddBlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Pattern string `json:"pattern"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Pattern == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := s.st.AddBlock(body.Pattern); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

func (s *Server) adminDeleteBlock(w http.ResponseWriter, pattern string) {
	if err := s.st.DeleteBlock(pattern); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

func (s *Server) adminFollowers(w http.ResponseWriter) {
	followers, err := s.st.ListFollowers()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, followers)
}

func (s *Server) adminDeleteFollower(w http.ResponseWriter, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	if err := s.st.DeleteFollowerByID(id); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

// adminPost is the administrative view of a stored post. It deliberately omits
// the rendered Note: administration is about identifying and withdrawing a
// post, not re-reading it.
type adminPost struct {
	ID          int64  `json:"id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	APID        string `json:"ap_id"`
	PublishedAt string `json:"published_at"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	DeletedAt   string `json:"deleted_at,omitempty"`
}

func (s *Server) adminPosts(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := pagination(r, 100, 200)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	posts, err := s.st.ListPostsForAdmin(limit, offset)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	rows := make([]adminPost, 0, len(posts))
	for _, p := range posts {
		rows = append(rows, adminPost{
			ID: p.ID, URL: p.URL, Title: p.Title, APID: p.APID.String,
			PublishedAt: p.PublishedAt, UpdatedAt: p.UpdatedAt.String,
			DeletedAt: p.DeletedAt.String,
		})
	}
	writeAdminJSON(w, rows)
}

// adminDeletePost withdraws a post and queues a Delete to every follower. It
// answers 200 for a repeat rather than an error so a retrying script can tell
// "already done" from "failed".
func (s *Server) adminDeletePost(w http.ResponseWriter, rawID string) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	result, err := s.st.DeleteFederatedPost(id, store.NowString(), func(p *store.Post) ([]byte, error) {
		return publish.Marshal(publish.Delete(s.cfg.Actor, p))
	})
	if errors.Is(err, store.ErrPostNotFound) {
		http.NotFound(w, nil)
		return
	}
	if err != nil {
		s.log.Error("delete post failed", "id", id, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if !result.AlreadyDeleted {
		s.log.Info("post deleted", "id", id, "ap_id", result.Post.APID.String,
			"queued", result.Queued)
	}
	writeAdminJSON(w, map[string]any{
		"ok":              true,
		"id":              id,
		"ap_id":           result.Post.APID.String,
		"deleted_at":      result.Post.DeletedAt.String,
		"already_deleted": result.AlreadyDeleted,
		"queued":          result.Queued,
	})
}

func (s *Server) adminDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit, offset, ok := pagination(r, 100, 200)
	if !ok || !store.ValidDeliveryStatus(status) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	deliveries, err := s.st.ListDeliveries(status, limit, offset)
	if err != nil {
		s.log.Error("list deliveries failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, deliveries)
}

// adminDeliveryAction applies a single-row queue mutation, mapping the store's
// two refusal reasons onto 404 (no such row) and 409 (wrong state).
func (s *Server) adminDeliveryAction(w http.ResponseWriter, rawID string, action func(int64) error) {
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch err := action(id); {
	case err == nil:
		writeAdminJSON(w, map[string]any{"ok": true, "id": id})
	case errors.Is(err, store.ErrDeliveryNotFound):
		http.NotFound(w, nil)
	case errors.Is(err, store.ErrDeliveryState):
		http.Error(w, "delivery is pending; the worker may be sending it", http.StatusConflict)
	default:
		s.log.Error("delivery action failed", "id", id, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
	}
}

func (s *Server) adminRetryFailedDeliveries(w http.ResponseWriter) {
	n, err := s.st.RetryFailedDeliveries()
	if err != nil {
		s.log.Error("bulk delivery retry failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.log.Info("failed deliveries requeued", "count", n)
	writeAdminJSON(w, map[string]any{"ok": true, "retried": n})
}

// adminPublishActor announces the daemon's currently loaded actor document to
// followers. The TOML configuration is the only source of profile data: this
// endpoint accepts no body, so no profile field or key material can be
// injected over the admin API.
//
// Publication never happens automatically. An operator edits the config,
// restarts the daemon so it loads the new values, then calls this.
func (s *Server) adminPublishActor(w http.ResponseWriter) {
	doc := s.ap.Document()
	activity, fingerprint, err := publish.ActorUpdate(s.cfg.Actor, doc)
	if err != nil {
		s.log.Error("build actor update failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	activityJSON, err := publish.Marshal(activity)
	if err != nil {
		s.log.Error("marshal actor update failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	result, err := s.st.PublishActorUpdate(fingerprint, activityJSON)
	if err != nil {
		s.log.Error("publish actor update failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if result.Published {
		s.log.Info("actor profile published", "fingerprint", fingerprint, "queued", result.Queued)
	}
	writeAdminJSON(w, map[string]any{
		"published":   result.Published,
		"fingerprint": result.Fingerprint,
		"queued":      result.Queued,
	})
}

// pagination reads limit/offset query parameters, applying a default and a cap
// so an administrative listing cannot be asked for an unbounded page.
func pagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int, ok bool) {
	limit, offset = defaultLimit, 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, 0, false
		}
		limit = min(n, maxLimit)
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func (s *Server) adminStats(w http.ResponseWriter) {
	followers, err := s.st.FollowerCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	posts, err := s.st.PostCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	interactions, err := s.st.InteractionCounts()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	pending, err := s.st.PendingDeliveryCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{
		"build":              buildinfo.Current(),
		"schema_version":     s.st.SchemaVersion(),
		"followers":          followers,
		"posts":              posts,
		"interactions":       interactions,
		"pending_deliveries": pending,
	})
}

func (s *Server) adminPoll(w http.ResponseWriter, r *http.Request) {
	if s.pollNow == nil {
		http.Error(w, "poller unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.pollNow(r.Context()); err != nil {
		s.log.Error("manual poll failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeAdminJSON(w, map[string]any{"ok": true})
}

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}
