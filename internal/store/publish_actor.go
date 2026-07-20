package store

import (
	"database/sql"
	"errors"
)

// ActorFingerprintKey names the state entry holding the fingerprint of the
// actor document most recently published to followers.
const ActorFingerprintKey = "actor.published_fingerprint"

// PublishActorResult reports whether an actor Update was queued. Published is
// false when the profile already matches what was last announced.
type PublishActorResult struct {
	Published   bool
	Fingerprint string
	Queued      int
}

// PublishActorUpdate announces an actor document to followers, unless that
// exact document was already announced.
//
// The fingerprint is stored in the same transaction as the delivery rows, so
// the state can never claim a profile was published when nothing was queued.
// With no followers there is nothing to queue, but the fingerprint is still
// recorded: the administrator has acknowledged the current representation.
func (s *Store) PublishActorUpdate(fingerprint string, activityJSON []byte) (*PublishActorResult, error) {
	result := &PublishActorResult{Fingerprint: fingerprint}
	queued, err := s.WithFanOut(func(tx *sql.Tx) ([]byte, error) {
		published, err := getStateTx(tx, ActorFingerprintKey)
		if err != nil {
			return nil, err
		}
		if published == fingerprint {
			return nil, nil
		}
		if err := setStateTx(tx, ActorFingerprintKey, fingerprint); err != nil {
			return nil, err
		}
		result.Published = true
		return activityJSON, nil
	})
	if err != nil {
		return nil, err
	}
	result.Queued = queued
	return result, nil
}

func getStateTx(tx *sql.Tx, key string) (string, error) {
	var v string
	err := tx.QueryRow(`SELECT value FROM state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func setStateTx(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`
		INSERT INTO state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
