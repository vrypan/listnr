package store

import (
	"database/sql"
	"errors"
)

// Namespaced state keys holding the completed migration. They are written
// together in one transaction and never rewritten.
const (
	MoveTargetKey      = "move.target"
	MoveActivityIDKey  = "move.activity_id"
	MoveFingerprintKey = "move.target_fingerprint"
	MoveMovedAtKey     = "move.moved_at"
)

// ErrAlreadyMovedElsewhere means a Move to a different target has already been
// published. Migration is announced to followers and cannot be taken back, so
// a second, contradictory target is refused rather than published.
var ErrAlreadyMovedElsewhere = errors.New("actor has already moved to a different target")

// Move records a completed actor migration.
type Move struct {
	Target            string `json:"target"`
	ActivityID        string `json:"activity_id"`
	TargetFingerprint string `json:"target_fingerprint"`
	MovedAt           string `json:"moved_at"`
}

// MoveOutcome classifies a migration request against the stored state.
type MoveOutcome string

const (
	// NotMoved means no migration has been published.
	NotMoved MoveOutcome = "not_moved"
	// MovedToSameTarget means this exact migration is already published, so
	// repeating it is a no-op.
	MovedToSameTarget MoveOutcome = "moved_to_same_target"
	// MovedToDifferentTarget means a contradictory migration exists.
	MovedToDifferentTarget MoveOutcome = "moved_to_different_target"
)

// MoveStatus reports the stored migration, and how a proposed target compares
// to it. Pass an empty target to query the state alone. The returned Move is
// nil when nothing has been published.
func (s *Store) MoveStatus(target string) (MoveOutcome, *Move, error) {
	move, err := s.CurrentMove()
	if err != nil {
		return "", nil, err
	}
	switch {
	case move == nil:
		return NotMoved, nil, nil
	case target == "" || move.Target == target:
		return MovedToSameTarget, move, nil
	default:
		return MovedToDifferentTarget, move, nil
	}
}

// CurrentMove returns the published migration, or nil if the actor has not
// moved.
func (s *Store) CurrentMove() (*Move, error) {
	target, err := s.GetState(MoveTargetKey)
	if err != nil || target == "" {
		return nil, err
	}
	move := &Move{Target: target}
	for _, field := range []struct {
		key   string
		value *string
	}{
		{MoveActivityIDKey, &move.ActivityID},
		{MoveFingerprintKey, &move.TargetFingerprint},
		{MoveMovedAtKey, &move.MovedAt},
	} {
		v, err := s.GetState(field.key)
		if err != nil {
			return nil, err
		}
		*field.value = v
	}
	return move, nil
}

// CommitMove persists a migration and fans the Move activity out to every
// deduplicated follower inbox in one transaction, so the actor can never be
// recorded as moved without its followers having been told.
//
// Repeating the same migration returns the stored state and enqueues nothing.
// A different target returns ErrAlreadyMovedElsewhere.
//
// The target must already have been fetched and validated: no HTTP request may
// happen inside this transaction.
func (s *Store) CommitMove(move Move, activityJSON []byte) (alreadyMoved bool, queued int, err error) {
	queued, err = s.WithFanOut(func(tx *sql.Tx) ([]byte, error) {
		existing, err := getStateTx(tx, MoveTargetKey)
		if err != nil {
			return nil, err
		}
		switch {
		case existing == move.Target:
			alreadyMoved = true
			return nil, nil
		case existing != "":
			return nil, ErrAlreadyMovedElsewhere
		}
		for _, field := range [][2]string{
			{MoveTargetKey, move.Target},
			{MoveActivityIDKey, move.ActivityID},
			{MoveFingerprintKey, move.TargetFingerprint},
			{MoveMovedAtKey, move.MovedAt},
		} {
			if err := setStateTx(tx, field[0], field[1]); err != nil {
				return nil, err
			}
		}
		return activityJSON, nil
	})
	if err != nil {
		return false, 0, err
	}
	return alreadyMoved, queued, nil
}
