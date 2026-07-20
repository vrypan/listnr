package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vrypan/listnr/internal/publish"
	"github.com/vrypan/listnr/internal/store"
)

// adminMoveStatus reports whether the actor has migrated, and where to.
func (s *Server) adminMoveStatus(w http.ResponseWriter) {
	move, err := s.st.CurrentMove()
	if err != nil {
		s.log.Error("read move state failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if move == nil {
		writeAdminJSON(w, map[string]any{"moved": false})
		return
	}
	writeAdminJSON(w, map[string]any{
		"moved":              true,
		"target":             move.Target,
		"activity_id":        move.ActivityID,
		"target_fingerprint": move.TargetFingerprint,
		"moved_at":           move.MovedAt,
	})
}

// adminMove publishes an actor migration. This is irreversible: followers are
// told to follow somebody else, and listnr cannot recall that.
//
// The target is dereferenced and required to name this actor in its
// alsoKnownAs before anything is written. Fetching happens outside the
// transaction; the transaction then records the migration and queues one Move
// per follower inbox together.
func (s *Server) adminMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Target == "" {
		http.Error(w, "bad request: target is required", http.StatusBadRequest)
		return
	}

	// Refuse a contradictory target before making any outbound request.
	outcome, existing, err := s.st.MoveStatus(body.Target)
	if err != nil {
		s.log.Error("read move state failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if outcome == store.MovedToDifferentTarget {
		http.Error(w, "actor has already moved to "+existing.Target, http.StatusConflict)
		return
	}
	if outcome == store.MovedToSameTarget {
		writeAdminJSON(w, moveResponse(existing, true, 0))
		return
	}

	target, err := s.fetch.FetchMoveTarget(r.Context(), body.Target, s.cfg.Actor.ID())
	if err != nil {
		s.log.Warn("move target validation failed", "target", body.Target, "err", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	move := store.Move{
		Target:            target.ID,
		ActivityID:        publish.MoveID(s.cfg.Actor, target.ID),
		TargetFingerprint: target.Fingerprint,
		MovedAt:           store.NowString(),
	}
	activityJSON, err := publish.Marshal(publish.Move(s.cfg.Actor, move.Target, move.MovedAt))
	if err != nil {
		s.log.Error("build move activity failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	alreadyMoved, queued, err := s.st.CommitMove(move, activityJSON)
	if errors.Is(err, store.ErrAlreadyMovedElsewhere) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		s.log.Error("commit move failed", "target", move.Target, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	// The actor document now advertises movedTo, while keeping its id, keys,
	// inbox and URLs so everything stays dereferenceable.
	s.ap.SetMovedTo(move.Target)
	if !alreadyMoved {
		s.log.Info("actor moved", "target", move.Target, "queued", queued)
	}
	writeAdminJSON(w, moveResponse(&move, alreadyMoved, queued))
}

func moveResponse(move *store.Move, alreadyMoved bool, queued int) map[string]any {
	return map[string]any{
		"ok":            true,
		"target":        move.Target,
		"activity_id":   move.ActivityID,
		"moved_at":      move.MovedAt,
		"already_moved": alreadyMoved,
		"queued":        queued,
	}
}

// RestoreMoveState re-applies a migration recorded in a previous run, so the
// actor document keeps advertising movedTo across restarts.
func (s *Server) RestoreMoveState() error {
	move, err := s.st.CurrentMove()
	if err != nil || move == nil {
		return err
	}
	s.ap.SetMovedTo(move.Target)
	return nil
}
