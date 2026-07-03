package store

func (s *Store) UpsertFollower(actorID, inbox, sharedInbox string) error {
	_, err := s.DB.Exec(`
		INSERT INTO followers (actor_id, inbox, shared_inbox)
		VALUES (?, ?, ?)
		ON CONFLICT(actor_id) DO UPDATE SET
			inbox=excluded.inbox, shared_inbox=excluded.shared_inbox`,
		actorID, inbox, sharedInbox)
	return err
}

func (s *Store) DeleteFollower(actorID string) error {
	_, err := s.DB.Exec(`DELETE FROM followers WHERE actor_id = ?`, actorID)
	return err
}

// DeleteFollowersWithInbox removes all followers reachable only through the
// given inbox URL (personal or shared). Used when an inbox returns 410 Gone.
func (s *Store) DeleteFollowersWithInbox(inboxURL string) error {
	_, err := s.DB.Exec(`DELETE FROM followers WHERE inbox = ? OR shared_inbox = ?`,
		inboxURL, inboxURL)
	return err
}

// DeliveryInboxes returns the distinct set of inbox URLs needed to reach all
// followers, preferring shared inboxes where available.
func (s *Store) DeliveryInboxes() ([]string, error) {
	rows, err := s.DB.Query(`
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
