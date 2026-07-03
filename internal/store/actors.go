package store

import (
	"database/sql"
	"errors"
	"time"
)

// CachedActor is a locally cached copy of a remote actor's profile.
type CachedActor struct {
	ID           string
	PublicKeyPEM string
	Name         string
	Handle       string
	IconURL      string
	Inbox        string
	SharedInbox  string
	FetchedAt    time.Time
}

// GetCachedActor returns the cached actor or nil if not cached.
func (s *Store) GetCachedActor(actorID string) (*CachedActor, error) {
	var a CachedActor
	var fetchedAt string
	err := s.DB.QueryRow(`
		SELECT actor_id, public_key_pem, name, handle, icon_url, inbox, shared_inbox, fetched_at
		FROM actor_cache WHERE actor_id = ?`, actorID).
		Scan(&a.ID, &a.PublicKeyPEM, &a.Name, &a.Handle, &a.IconURL, &a.Inbox, &a.SharedInbox, &fetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.FetchedAt, _ = time.Parse(time.RFC3339, fetchedAt)
	return &a, nil
}

func (s *Store) UpsertCachedActor(a *CachedActor) error {
	_, err := s.DB.Exec(`
		INSERT INTO actor_cache (actor_id, public_key_pem, name, handle, icon_url, inbox, shared_inbox, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(actor_id) DO UPDATE SET
			public_key_pem=excluded.public_key_pem, name=excluded.name,
			handle=excluded.handle, icon_url=excluded.icon_url,
			inbox=excluded.inbox, shared_inbox=excluded.shared_inbox,
			fetched_at=excluded.fetched_at`,
		a.ID, a.PublicKeyPEM, a.Name, a.Handle, a.IconURL, a.Inbox, a.SharedInbox,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) DeleteCachedActor(actorID string) error {
	_, err := s.DB.Exec(`DELETE FROM actor_cache WHERE actor_id = ?`, actorID)
	return err
}

// PurgeActor removes every trace of a remote actor: interactions, follower
// row, and cache entry. Used for actor-level Delete activities.
func (s *Store) PurgeActor(actorID string) error {
	for _, q := range []string{
		`DELETE FROM interactions WHERE actor_id = ?`,
		`DELETE FROM followers WHERE actor_id = ?`,
		`DELETE FROM actor_cache WHERE actor_id = ?`,
	} {
		if _, err := s.DB.Exec(q, actorID); err != nil {
			return err
		}
	}
	return nil
}
