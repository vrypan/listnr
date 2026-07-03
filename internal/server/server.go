package server

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/store"
)

type Server struct {
	cfg *config.Config
	st  *store.Store
	ap  *ap.Handler
	log *slog.Logger
}

func New(cfg *config.Config, st *store.Store, apHandler *ap.Handler, log *slog.Logger) *Server {
	return &Server{cfg: cfg, st: st, ap: apHandler, log: log}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/webfinger", s.ap.ServeWebfinger)
	mux.HandleFunc("GET /actor", s.ap.ServeActor)
	mux.HandleFunc("POST /inbox", s.handleInbox)
	mux.HandleFunc("GET /outbox", s.handleOutbox)
	mux.HandleFunc("GET /followers", s.handleFollowers)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return s.logRequests(mux)
}

// handleInbox is a stub: it acknowledges deliveries without processing them.
// Signature verification and activity dispatch land in milestone 2.
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	s.log.Info("inbox activity received (ignored: not implemented)",
		"bytes", len(body), "from", r.RemoteAddr)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.PostCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.writeCollection(w, "/outbox", n)
}

func (s *Server) handleFollowers(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.FollowerCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.writeCollection(w, "/followers", n)
}

// writeCollection serves a count-only OrderedCollection; item pages come
// with milestone 2.
func (s *Server) writeCollection(w http.ResponseWriter, path string, total int) {
	ap.WriteJSON(w, ap.ContentType, map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"id":         "https://" + s.cfg.Actor.Host + path,
		"type":       "OrderedCollection",
		"totalItems": total,
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		s.log.Debug("request", "method", r.Method, "path", r.URL.Path)
	})
}
