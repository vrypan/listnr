package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/httpsig"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

const maxInboxBody = 1 << 20

// stringOrID unmarshals both `"actor": "https://..."` and
// `"actor": {"id": "https://..."}`.
type stringOrID string

func (s *stringOrID) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = stringOrID(str)
		return nil
	}
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*s = stringOrID(obj.ID)
	return nil
}

type envelope struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Actor     stringOrID      `json:"actor"`
	Object    json.RawMessage `json:"object"`
	Published string          `json:"published"`
}

// object is the inner object of an activity, for the shapes we care about.
type object struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Actor     stringOrID `json:"actor"`  // inner activity (Undo)
	Object    stringOrID `json:"object"` // inner target (Undo Follow/Like)
	InReplyTo stringOrID `json:"inReplyTo"`
	Content   string     `json:"content"`
	Published string     `json:"published"`
}

// decodeObject parses the activity's object, tolerating the bare-string form
// (in which case only ID is set).
func decodeObject(raw json.RawMessage) (*object, error) {
	if len(raw) == 0 {
		return &object{}, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &object{ID: s}, nil
	}
	var o object
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxInboxBody))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Drop blocked actors before verifying, so a blocked instance can't make
	// us emit an outbound key-fetch on every message. verify() later pins the
	// activity actor to the signing-key owner, so env.Actor is authoritative
	// for anything that gets dispatched.
	blocked, err := s.st.IsBlocked(string(env.Actor))
	if err != nil {
		s.log.Error("block check failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if blocked {
		// Accept and drop silently: don't reveal blocks to the sender.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	actor, ok := s.verify(ctx, w, r, body, &env)
	if !ok {
		return // response already written
	}

	// Reject replays of an already-processed activity. Checked after verify
	// so an attacker can't evict a pending id with an unsigned request.
	if env.ID != "" {
		seen, err := s.st.ActivitySeen(env.ID)
		if err != nil {
			s.log.Error("replay check failed", "err", err)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if seen {
			w.WriteHeader(http.StatusAccepted)
			return
		}
	}

	if err := s.dispatch(ctx, &env, actor); err != nil {
		s.log.Error("inbox dispatch failed", "type", env.Type, "actor", env.Actor, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	// Mark seen only after success, so a transient failure can be retried.
	if env.ID != "" {
		if err := s.st.MarkActivitySeen(env.ID); err != nil {
			s.log.Warn("recording seen activity failed", "id", env.ID, "err", err)
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

// verify authenticates the request: signature parse, key fetch (with one
// cache-bypassing retry for rotated keys), digest/date checks, and
// actor-matches-key check. On failure it writes the HTTP response and
// returns ok=false. A Delete for an already-gone actor is handled here
// (purge + 202) since its signature can no longer be verified.
func (s *Server) verify(ctx context.Context, w http.ResponseWriter, r *http.Request, body []byte, env *envelope) (*fedi.Actor, bool) {
	sig, err := httpsig.ParseSignature(r.Header.Get("Signature"))
	if err != nil {
		http.Error(w, "signature required", http.StatusUnauthorized)
		return nil, false
	}

	keyActor, err := s.fetch.FetchActor(ctx, sig.KeyID, false)
	if errors.Is(err, fedi.ErrGone) {
		// The signing key is gone, so we can't verify anything. The only
		// activity we honor here is an actor deleting itself: a Delete
		// whose object IS the actor, where the (now-gone) key belongs to
		// that same actor. Anything else is refused — otherwise anyone
		// could purge an arbitrary actor by pointing keyId at a URL that
		// 404s.
		obj, _ := decodeObject(env.Object)
		if env.Type == "Delete" && env.Actor != "" &&
			obj != nil && obj.ID == string(env.Actor) &&
			sameHost(sig.KeyID, string(env.Actor)) {
			s.st.PurgeActor(string(env.Actor))
			w.WriteHeader(http.StatusAccepted)
		} else {
			http.Error(w, "key owner gone", http.StatusUnauthorized)
		}
		return nil, false
	}
	if err != nil {
		s.log.Warn("key owner fetch failed", "keyId", sig.KeyID, "err", err)
		http.Error(w, "cannot fetch signing key", http.StatusUnauthorized)
		return nil, false
	}

	verifyWith := func(a *fedi.Actor) error {
		pub, err := keys.ParsePublicPEM(a.PublicKeyPEM)
		if err != nil {
			return err
		}
		return httpsig.Verify(r, body, pub, sig)
	}
	if err := verifyWith(keyActor); err != nil {
		// Retry once with a fresh fetch, in case the key was rotated.
		keyActor, err = s.fetch.FetchActor(ctx, sig.KeyID, true)
		if err != nil || verifyWith(keyActor) != nil {
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
			return nil, false
		}
	}

	if string(env.Actor) != keyActor.ID {
		http.Error(w, "activity actor does not match signing key owner", http.StatusUnauthorized)
		return nil, false
	}
	return keyActor, true
}

func (s *Server) dispatch(ctx context.Context, env *envelope, actor *fedi.Actor) error {
	obj, err := decodeObject(env.Object)
	if err != nil {
		return err
	}
	switch env.Type {
	case "Follow":
		return s.handleFollow(env, obj, actor)
	case "Undo":
		return s.handleUndo(obj, actor)
	case "Like":
		return s.storeSimpleInteraction(env, obj, actor, "like")
	case "Announce":
		return s.storeSimpleInteraction(env, obj, actor, "boost")
	case "Create":
		return s.handleCreate(env, obj, actor)
	case "Update":
		return s.handleUpdateNote(obj, actor)
	case "Delete":
		return s.handleDelete(obj, actor)
	default:
		s.log.Debug("ignoring activity", "type", env.Type, "actor", actor.ID)
		return nil
	}
}

func (s *Server) handleFollow(env *envelope, obj *object, actor *fedi.Actor) error {
	if obj.ID != s.cfg.Actor.ID() {
		s.log.Debug("follow for unknown object ignored", "object", obj.ID)
		return nil
	}
	// After a Move the old identity accepts no new followers: they would be
	// following an account that has already told everyone to go elsewhere. The
	// request is still acknowledged with 202, like any other ignored activity.
	// Undo and Delete keep working, so existing followers can still leave.
	move, err := s.st.CurrentMove()
	if err != nil {
		return err
	}
	if move != nil {
		s.log.Info("follow ignored: actor has moved", "target", move.Target)
		return nil
	}
	if err := s.st.UpsertFollower(actor.ID, actor.Inbox, actor.SharedInbox); err != nil {
		return err
	}
	accept, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       "https://" + s.cfg.Actor.Host + "/activities/" + randomID(),
		"type":     "Accept",
		"actor":    s.cfg.Actor.ID(),
		"object":   json.RawMessage(mustMarshalEnvelope(env)),
	})
	if err != nil {
		return err
	}
	s.log.Info("new follower", "actor", actor.ID, "handle", actor.Handle)
	// Accepts go to the follower's personal inbox, not the shared one.
	return s.deliver.Enqueue(accept, actor.Inbox)
}

func (s *Server) handleUndo(obj *object, actor *fedi.Actor) error {
	switch obj.Type {
	case "Follow":
		s.log.Info("follower left", "actor", actor.ID)
		return s.st.DeleteFollower(actor.ID)
	case "Like", "Announce":
		if obj.ID != "" {
			return s.st.DeleteInteractionByAPID(obj.ID, actor.ID)
		}
		kind := map[string]string{"Like": "like", "Announce": "boost"}[obj.Type]
		if postID, ok, err := s.resolve(string(obj.Object)); err != nil {
			return err
		} else if ok {
			return s.st.DeleteInteractionByActorKindPost(actor.ID, kind, postID)
		}
		return nil
	default:
		// The inner object may be a bare activity-id string (obj.Type
		// empty): try deleting any interaction with that id.
		if obj.ID != "" {
			return s.st.DeleteInteractionByAPID(obj.ID, actor.ID)
		}
		return nil
	}
}

func (s *Server) storeSimpleInteraction(env *envelope, obj *object, actor *fedi.Actor, kind string) error {
	postID, ok, err := s.resolve(obj.ID)
	if err != nil || !ok {
		return err
	}
	_, err = s.st.InsertInteraction(&store.Interaction{
		APID:         env.ID,
		Kind:         kind,
		PostID:       postID,
		ActorID:      actor.ID,
		ActorHandle:  actor.Handle,
		ActorName:    actor.Name,
		ActorIconURL: actor.IconURL,
		Published:    publishedOrNow(env.Published),
	})
	if err == nil {
		s.log.Info("interaction", "kind", kind, "actor", actor.Handle)
	}
	return err
}

func (s *Server) handleCreate(env *envelope, obj *object, actor *fedi.Actor) error {
	if obj.Type != "Note" || obj.InReplyTo == "" || obj.ID == "" {
		return nil
	}
	postID, ok, err := s.resolve(string(obj.InReplyTo))
	if err != nil || !ok {
		return err
	}
	_, err = s.st.InsertInteraction(&store.Interaction{
		APID:         obj.ID,
		Kind:         "reply",
		PostID:       postID,
		ActorID:      actor.ID,
		ActorHandle:  actor.Handle,
		ActorName:    actor.Name,
		ActorIconURL: actor.IconURL,
		ContentHTML:  s.sanitize.Sanitize(obj.Content),
		InReplyTo:    string(obj.InReplyTo),
		Published:    publishedOrNow(obj.Published),
	})
	if err == nil {
		s.log.Info("reply received", "actor", actor.Handle, "note", obj.ID)
	}
	return err
}

func (s *Server) handleUpdateNote(obj *object, actor *fedi.Actor) error {
	if obj.Type != "Note" || obj.ID == "" {
		return nil
	}
	return s.st.UpdateInteractionContent(obj.ID, actor.ID,
		s.sanitize.Sanitize(obj.Content), publishedOrNow(obj.Published))
}

func (s *Server) handleDelete(obj *object, actor *fedi.Actor) error {
	if obj.ID == "" {
		return nil
	}
	if obj.ID == actor.ID {
		s.log.Info("actor deleted, purging", "actor", actor.ID)
		return s.st.PurgeActor(actor.ID)
	}
	return s.st.DeleteInteractionByAPID(obj.ID, actor.ID)
}

// resolve maps a URL from an incoming activity to a local post id.
func (s *Server) resolve(url string) (int64, bool, error) {
	if url == "" {
		return 0, false, nil
	}
	return s.st.ResolvePost(url)
}

func publishedOrNow(published string) string {
	if published != "" {
		return published
	}
	return time.Now().UTC().Format(time.RFC3339)
}

// sameHost reports whether two URLs share a (case-insensitive) host. Used to
// confirm a gone signing key belongs to the actor it claims to delete.
func sameHost(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil || ua.Host == "" {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Host, ub.Host)
}

// mustMarshalEnvelope re-serializes the received activity for embedding in
// an Accept. Marshalling a struct we just unmarshalled cannot fail.
func mustMarshalEnvelope(env *envelope) []byte {
	b, _ := json.Marshal(map[string]any{
		"id":     env.ID,
		"type":   env.Type,
		"actor":  string(env.Actor),
		"object": env.Object,
	})
	return b
}
