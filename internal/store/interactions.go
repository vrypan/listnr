package store

import (
	"database/sql"
	"errors"
)

type Interaction struct {
	ID           int64  `json:"id"`
	APID         string `json:"ap_id"`
	Kind         string `json:"kind"` // reply | like | boost
	PostID       int64  `json:"post_id"`
	ActorID      string `json:"actor_id"`
	ActorHandle  string `json:"actor_handle"`
	ActorName    string `json:"actor_name"`
	ActorIconURL string `json:"actor_icon_url"`
	ContentHTML  string `json:"content_html"`
	Published    string `json:"published"`
	ReceivedAt   string `json:"received_at"`
	Hidden       bool   `json:"hidden"`
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
// matches either the post's ActivityPub Note id, its blog permalink, or a
// stored reply Note id that already belongs to that post.
// Returns (0, false) when the URL is not one of our posts.
func (s *Store) ResolvePost(url string) (int64, bool, error) {
	var id int64
	err := s.DB.QueryRow(`
		SELECT id FROM posts WHERE ap_id = ? OR url = ?
		UNION
		SELECT post_id FROM interactions WHERE kind = 'reply' AND ap_id = ?
		LIMIT 1`, url, url, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *Store) ListReplies(postURL string, hiddenOnly bool) ([]Interaction, error) {
	query := `
		SELECT i.id, i.ap_id, i.kind, i.post_id, i.actor_id, i.actor_handle,
		       i.actor_name, i.actor_icon_url, i.content_html, i.published_at,
		       i.received_at, i.hidden
		FROM interactions i
		JOIN posts p ON p.id = i.post_id
		WHERE i.kind = 'reply'`
	var args []any
	if postURL != "" {
		query += ` AND p.url = ?`
		args = append(args, postURL)
	}
	if hiddenOnly {
		query += ` AND i.hidden = 1`
	}
	query += ` ORDER BY i.received_at DESC, i.id DESC`
	return s.scanInteractions(query, args...)
}

func (s *Store) HideInteraction(id int64, hidden bool) error {
	v := 0
	if hidden {
		v = 1
	}
	_, err := s.DB.Exec(`UPDATE interactions SET hidden = ? WHERE id = ?`, v, id)
	return err
}

func (s *Store) DeleteInteractionByID(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM interactions WHERE id = ?`, id)
	return err
}

func (s *Store) InteractionCounts() (map[string]int, error) {
	rows, err := s.DB.Query(`SELECT kind, COUNT(*) FROM interactions GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{"reply": 0, "like": 0, "boost": 0}
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, err
		}
		counts[kind] = n
	}
	return counts, rows.Err()
}

func (s *Store) VisibleInteractionsForPost(postID int64) ([]Interaction, error) {
	return s.scanInteractions(`
		SELECT id, ap_id, kind, post_id, actor_id, actor_handle, actor_name,
		       actor_icon_url, content_html, published_at, received_at, hidden
		FROM interactions
		WHERE post_id = ? AND hidden = 0
		ORDER BY CASE kind WHEN 'reply' THEN published_at ELSE received_at END ASC, id ASC`, postID)
}

func (s *Store) scanInteractions(query string, args ...any) ([]Interaction, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interaction
	for rows.Next() {
		var i Interaction
		var hidden int
		if err := rows.Scan(&i.ID, &i.APID, &i.Kind, &i.PostID, &i.ActorID,
			&i.ActorHandle, &i.ActorName, &i.ActorIconURL, &i.ContentHTML,
			&i.Published, &i.ReceivedAt, &hidden); err != nil {
			return nil, err
		}
		i.Hidden = hidden != 0
		out = append(out, i)
	}
	return out, rows.Err()
}
