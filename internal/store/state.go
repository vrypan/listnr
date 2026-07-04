package store

import (
	"database/sql"
	"errors"
	"time"
)

func (s *Store) GetState(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(key, value string) error {
	_, err := s.DB.Exec(`
		INSERT INTO state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// ActivitySeen reports whether an inbox activity id has already been
// processed, used to reject replayed (captured-and-resent) signed requests.
func (s *Store) ActivitySeen(activityID string) (bool, error) {
	var one int
	err := s.DB.QueryRow(`SELECT 1 FROM seen_activities WHERE activity_id = ?`, activityID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// MarkActivitySeen records an activity id as processed. Called after a
// successful dispatch so a transient failure can still be retried.
func (s *Store) MarkActivitySeen(activityID string) error {
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO seen_activities (activity_id) VALUES (?)`, activityID)
	return err
}

// CleanupSeenActivities drops seen-activity rows older than maxAge; ids only
// need to outlive the signature clock-skew window.
func (s *Store) CleanupSeenActivities(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge).UTC().Format("2006-01-02T15:04:05Z")
	_, err := s.DB.Exec(`DELETE FROM seen_activities WHERE seen_at < ?`, cutoff)
	return err
}
