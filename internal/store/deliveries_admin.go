package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	// ErrDeliveryNotFound means no delivery row carries the requested id.
	ErrDeliveryNotFound = errors.New("delivery not found")
	// ErrDeliveryState means the row exists but its current status does not
	// allow the requested action.
	ErrDeliveryState = errors.New("delivery is not in a state that allows this")
)

// AdminDelivery is the administrative view of a queued delivery. It carries
// the activity's type and id but never the activity JSON itself: outbound
// payloads can embed inbound-derived content, so the full document stays out
// of admin responses and logs.
type AdminDelivery struct {
	ID            int64  `json:"id"`
	InboxURL      string `json:"inbox_url"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at"`
	LastError     string `json:"last_error,omitempty"`
	ActivityType  string `json:"activity_type,omitempty"`
	ActivityID    string `json:"activity_id,omitempty"`
}

// ValidDeliveryStatus reports whether status is one the queue uses. An empty
// string means "any status".
func ValidDeliveryStatus(status string) bool {
	switch status {
	case "", "pending", "done", "failed":
		return true
	}
	return false
}

// ListDeliveries returns deliveries newest first. The queue has no created_at
// column, but ids are assigned in insertion order, so descending id is the
// insertion order reversed.
func (s *Store) ListDeliveries(status string, limit, offset int) ([]AdminDelivery, error) {
	if !ValidDeliveryStatus(status) {
		return nil, errors.New("unknown delivery status " + status)
	}
	query := `
		SELECT id, activity_json, inbox_url, status, attempts, next_attempt_at, last_error
		FROM deliveries`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deliveries := []AdminDelivery{}
	for rows.Next() {
		var d AdminDelivery
		var activityJSON string
		if err := rows.Scan(&d.ID, &activityJSON, &d.InboxURL, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError); err != nil {
			return nil, err
		}
		d.ActivityType, d.ActivityID = activityMetadata(activityJSON)
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

// activityMetadata extracts just enough of an activity to identify it. A row
// whose payload will not parse still lists, with empty metadata, so a corrupt
// entry stays visible and deletable rather than hiding the whole page.
func activityMetadata(activityJSON string) (activityType, activityID string) {
	var doc struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(activityJSON), &doc); err != nil {
		return "", ""
	}
	return doc.Type, doc.ID
}

// RetryDelivery returns one failed delivery to the queue. Attempts reset to
// zero on purpose: an operator retrying after fixing DNS or TLS is asking for
// a fresh retry budget, the original one having been spent on a broken remote.
//
// Pending rows are refused because the worker may already be sending them.
func (s *Store) RetryDelivery(id int64) error {
	return s.mutateDelivery(id, `
		UPDATE deliveries
		SET status = 'pending', attempts = 0, next_attempt_at = ?, last_error = ''
		WHERE id = ? AND status = 'failed'`, ts(time.Now()), id)
}

// RetryFailedDeliveries returns every failed delivery to the queue in one
// transaction and reports how many moved.
func (s *Store) RetryFailedDeliveries() (int, error) {
	res, err := s.DB.Exec(`
		UPDATE deliveries
		SET status = 'pending', attempts = 0, next_attempt_at = ?, last_error = ''
		WHERE status = 'failed'`, ts(time.Now()))
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	return int(affected), err
}

// DeleteDelivery removes one terminal delivery row. Pending rows are refused:
// the worker may be sending them, and dropping the row would not stop it.
func (s *Store) DeleteDelivery(id int64) error {
	return s.mutateDelivery(id, `
		DELETE FROM deliveries WHERE id = ? AND status IN ('failed','done')`, id)
}

// mutateDelivery applies a state-guarded statement and turns "nothing changed"
// into the reason it did not: either the row is absent or it is in the wrong
// state. The distinction is what lets the API answer 404 versus 409.
func (s *Store) mutateDelivery(id int64, statement string, args ...any) error {
	res, err := s.DB.Exec(statement, args...)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var exists int
	err = s.DB.QueryRow(`SELECT 1 FROM deliveries WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeliveryNotFound
	}
	if err != nil {
		return err
	}
	return ErrDeliveryState
}
