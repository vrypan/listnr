package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vrypan/listnr/internal/backup"
	"github.com/vrypan/listnr/internal/buildinfo"
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
