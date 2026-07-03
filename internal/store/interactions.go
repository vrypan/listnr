package store

import (
	"database/sql"
	"errors"
)

type Interaction struct {
	APID         string
	Kind         string // reply | like | boost
	PostID       int64
	ActorID      string
	ActorHandle  string
	ActorName    string
	ActorIconURL string
	ContentHTML  string
	Published    string
}

// InsertInteraction stores an interaction; duplicates (same ap_id) are
// silently ignored. Returns whether a row was inserted.
func (s *Store) InsertInteraction(i *Interaction) (bool, error) {
	res, err := s.DB.Exec(`
		INSERT OR IGNORE INTO interactions
			(ap_id, kind, post_id, actor_id, actor_handle, actor_name,
			 actor_icon_url, content_html, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.APID, i.Kind, i.PostID, i.ActorID, i.ActorHandle, i.ActorName,
		i.ActorIconURL, i.ContentHTML, i.Published)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteInteractionByAPID removes an interaction, but only if it belongs to
// actorID — a remote actor can only retract their own interactions.
func (s *Store) DeleteInteractionByAPID(apID, actorID string) error {
	_, err := s.DB.Exec(`DELETE FROM interactions WHERE ap_id = ? AND actor_id = ?`,
		apID, actorID)
	return err
}

// DeleteInteractionByActorKindPost is the fallback for Undo activities whose
// inner object is only a URL we don't recognize.
func (s *Store) DeleteInteractionByActorKindPost(actorID, kind string, postID int64) error {
	_, err := s.DB.Exec(`DELETE FROM interactions WHERE actor_id = ? AND kind = ? AND post_id = ?`,
		actorID, kind, postID)
	return err
}

// UpdateInteractionContent updates a reply's content, only for its own actor.
func (s *Store) UpdateInteractionContent(apID, actorID, contentHTML, published string) error {
	_, err := s.DB.Exec(`
		UPDATE interactions SET content_html = ?, published_at = ?
		WHERE ap_id = ? AND actor_id = ?`,
		contentHTML, published, apID, actorID)
	return err
}

// ResolvePost maps a URL/id from an incoming activity to a local post: it
// matches either the post's ActivityPub Note id or its blog permalink.
// Returns (0, false) when the URL is not one of our posts.
func (s *Store) ResolvePost(url string) (int64, bool, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM posts WHERE ap_id = ? OR url = ?`, url, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}
