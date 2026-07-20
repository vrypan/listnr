package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"

	"github.com/microcosm-cc/bluemonday"
	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/store"
)

// ActorFetcher resolves remote actors (implemented by fedi.Client).
type ActorFetcher interface {
	FetchActor(ctx context.Context, actorID string, bypassCache bool) (*fedi.Actor, error)
}

// Deliverer queues outbound activities (implemented by delivery.Queue).
type Deliverer interface {
	Enqueue(activityJSON []byte, inboxURL string) error
	FanOut(activityJSON []byte) error
}

type Server struct {
	cfg        *config.Config
	configPath string
	st         *store.Store
	ap         *ap.Handler
	fetch      ActorFetcher
	deliver    Deliverer
	pollNow    func(context.Context) error
	sanitize   *bluemonday.Policy
	log        *slog.Logger
}

func New(cfg *config.Config, st *store.Store, apHandler *ap.Handler,
	fetch ActorFetcher, deliver Deliverer, log *slog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		st:       st,
		ap:       apHandler,
		fetch:    fetch,
		deliver:  deliver,
		sanitize: bluemonday.UGCPolicy(),
		log:      log,
	}
}

func (s *Server) SetPollFunc(fn func(context.Context) error) {
	s.pollNow = fn
}

func (s *Server) SetConfigPath(path string) {
	s.configPath = path
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/webfinger", s.ap.ServeWebfinger)
	mux.HandleFunc("GET /actor", s.ap.ServeActor)
	mux.HandleFunc("POST /inbox", s.handleInbox)
	mux.HandleFunc("GET /outbox", s.handleOutbox)
	mux.HandleFunc("GET /followers", s.handleFollowers)
	mux.HandleFunc("GET /posts/{id}", s.handlePost)
	mux.HandleFunc("GET /api/interactions", s.handleInteractions)
	mux.HandleFunc("/admin/", s.handleAdmin)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if s.cfg.Server.LogRequests {
		return s.logRequests(mux)
	}
	return mux
}

func (s *Server) handleOutbox(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.PostCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	page := r.URL.Query().Get("page")
	if page == "" {
		doc := map[string]any{
			"@context":   "https://www.w3.org/ns/activitystreams",
			"id":         "https://" + s.cfg.Actor.Host + "/outbox",
			"type":       "OrderedCollection",
			"totalItems": n,
		}
		if n > 0 {
			doc["first"] = "https://" + s.cfg.Actor.Host + "/outbox?page=1"
		}
		s.writeAP(w, r, doc)
		return
	}
	s.handleOutboxPage(w, r, n)
}

func (s *Server) handleFollowers(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.FollowerCount()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	s.writeCollection(w, r, "/followers", n)
}

// writeCollection serves a count-only OrderedCollection; the outbox gains
// item pages in milestone 3.
func (s *Server) writeCollection(w http.ResponseWriter, r *http.Request, path string, total int) {
	s.writeAP(w, r, map[string]any{
		"@context":   "https://www.w3.org/ns/activitystreams",
		"id":         "https://" + s.cfg.Actor.Host + path,
		"type":       "OrderedCollection",
		"totalItems": total,
	})
}

// randomID returns a 128-bit random hex string for activity ids.
func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
