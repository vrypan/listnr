package store

import (
	"database/sql"
	"time"
)

// WithFanOut runs mutate inside one transaction and enqueues the activity JSON
// it returns to every deduplicated follower inbox before committing. The state
// change and its deliveries therefore commit together: a crash can never leave
// a post marked deleted, or a profile marked published, with nothing queued.
//
// Returning nil activity JSON commits the mutation without enqueuing anything,
// which is how callers record a state change that has no audience.
//
// mutate must not perform HTTP requests; delivery happens later, out of band.
func (s *Store) WithFanOut(mutate func(*sql.Tx) ([]byte, error)) (queued int, err error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	activityJSON, err := mutate(tx)
	if err != nil {
		return 0, err
	}
	if activityJSON != nil {
		queued, err = fanOutTx(tx, string(activityJSON))
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return queued, nil
}

// fanOutTx inserts one pending delivery per distinct follower inbox, preferring
// shared inboxes so a remote instance receives the activity once.
func fanOutTx(tx *sql.Tx, activityJSON string) (int, error) {
	inboxes, err := deliveryInboxesTx(tx)
	if err != nil {
		return 0, err
	}
	now := ts(time.Now())
	for _, inbox := range inboxes {
		if _, err := tx.Exec(`
			INSERT INTO deliveries (activity_json, inbox_url, next_attempt_at)
			VALUES (?, ?, ?)`, activityJSON, inbox, now); err != nil {
			return 0, err
		}
	}
	return len(inboxes), nil
}

// deliveryInboxesTx reads the inbox set fully before the caller writes, because
// the store holds a single SQLite connection and cannot interleave a live
// cursor with inserts.
func deliveryInboxesTx(tx *sql.Tx) ([]string, error) {
	rows, err := tx.Query(`
		SELECT DISTINCT CASE WHEN shared_inbox <> '' THEN shared_inbox ELSE inbox END
		FROM followers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inboxes []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		inboxes = append(inboxes, u)
	}
	return inboxes, rows.Err()
}
