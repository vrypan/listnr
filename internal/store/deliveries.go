package store

import "time"

type Delivery struct {
	ID           int64
	ActivityJSON string
	InboxURL     string
	Attempts     int
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func (s *Store) EnqueueDelivery(activityJSON, inboxURL string) error {
	_, err := s.DB.Exec(`
		INSERT INTO deliveries (activity_json, inbox_url, next_attempt_at)
		VALUES (?, ?, ?)`,
		activityJSON, inboxURL, ts(time.Now()))
	return err
}

// DueDeliveries returns pending deliveries whose next attempt is due.
func (s *Store) DueDeliveries(limit int) ([]Delivery, error) {
	rows, err := s.DB.Query(`
		SELECT id, activity_json, inbox_url, attempts FROM deliveries
		WHERE status = 'pending' AND next_attempt_at <= ?
		ORDER BY next_attempt_at LIMIT ?`, ts(time.Now()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var due []Delivery
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.ActivityJSON, &d.InboxURL, &d.Attempts); err != nil {
			return nil, err
		}
		due = append(due, d)
	}
	return due, rows.Err()
}

func (s *Store) MarkDeliveryDone(id int64) error {
	_, err := s.DB.Exec(`UPDATE deliveries SET status='done', next_attempt_at=? WHERE id=?`,
		ts(time.Now()), id)
	return err
}

func (s *Store) MarkDeliveryFailed(id int64, lastError string) error {
	_, err := s.DB.Exec(`
		UPDATE deliveries SET status='failed', last_error=?, next_attempt_at=? WHERE id=?`,
		lastError, ts(time.Now()), id)
	return err
}

func (s *Store) RescheduleDelivery(id int64, attempts int, next time.Time, lastError string) error {
	_, err := s.DB.Exec(`
		UPDATE deliveries SET attempts=?, next_attempt_at=?, last_error=? WHERE id=?`,
		attempts, ts(next), lastError, id)
	return err
}

// CleanupDeliveries drops finished rows untouched for longer than maxAge.
func (s *Store) CleanupDeliveries(maxAge time.Duration) error {
	_, err := s.DB.Exec(`
		DELETE FROM deliveries
		WHERE status IN ('done','failed') AND next_attempt_at < ?`,
		ts(time.Now().Add(-maxAge)))
	return err
}

func (s *Store) PendingDeliveryCount() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM deliveries WHERE status = 'pending'`).Scan(&n)
	return n, err
}
