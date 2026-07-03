package store

type Follower struct {
	ID          int64  `json:"id"`
	ActorID     string `json:"actor_id"`
	Inbox       string `json:"inbox"`
	SharedInbox string `json:"shared_inbox"`
	FollowedAt  string `json:"followed_at"`
}

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

func (s *Store) DeleteFollowerByID(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM followers WHERE id = ?`, id)
	return err
}

func (s *Store) ListFollowers() ([]Follower, error) {
	rows, err := s.DB.Query(`
		SELECT id, actor_id, inbox, shared_inbox, followed_at
		FROM followers ORDER BY followed_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var followers []Follower
	for rows.Next() {
		var f Follower
		if err := rows.Scan(&f.ID, &f.ActorID, &f.Inbox, &f.SharedInbox, &f.FollowedAt); err != nil {
			return nil, err
		}
		followers = append(followers, f)
	}
	return followers, rows.Err()
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
