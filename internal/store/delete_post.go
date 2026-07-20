package store

import (
	"database/sql"
	"errors"
)

// ErrPostNotFound means no federated post carries the requested store id. A
// row that was ingested but never federated has no AP id to withdraw, so it
// is reported the same way.
var ErrPostNotFound = errors.New("federated post not found")

// DeletePostResult describes the outcome of a withdrawal attempt. Post always
// carries the stored deletion timestamp, whether this call set it or an
// earlier one did.
type DeletePostResult struct {
	Post           *Post
	AlreadyDeleted bool
	Queued         int
}

// DeleteFederatedPost withdraws one federated post and fans a Delete activity
// out to followers atomically. build receives the post with its new deletion
// timestamp already applied, so the activity it returns can quote that exact
// timestamp.
//
// The call is idempotent: repeating it on an already-withdrawn post neither
// moves the timestamp nor enqueues a second Delete. That check, rather than a
// delivery-table constraint, is what guarantees one Delete per inbox.
func (s *Store) DeleteFederatedPost(id int64, deletedAt string, build func(*Post) ([]byte, error)) (*DeletePostResult, error) {
	var result DeletePostResult
	queued, err := s.WithFanOut(func(tx *sql.Tx) ([]byte, error) {
		post, err := getPostTx(tx, id)
		if err != nil {
			return nil, err
		}
		if post == nil || !post.APID.Valid {
			return nil, ErrPostNotFound
		}
		if post.Deleted() {
			result.Post = post
			result.AlreadyDeleted = true
			return nil, nil
		}
		if _, err := tx.Exec(`UPDATE posts SET deleted_at = ? WHERE id = ?`, deletedAt, id); err != nil {
			return nil, err
		}
		post.DeletedAt = NullString(deletedAt)
		result.Post = post
		return build(post)
	})
	if err != nil {
		return nil, err
	}
	result.Queued = queued
	return &result, nil
}

func getPostTx(tx *sql.Tx, id int64) (*Post, error) {
	var p Post
	err := tx.QueryRow(`SELECT `+postColumns+` FROM posts WHERE id = ?`, id).
		Scan(&p.ID, &p.GUID, &p.URL, &p.Title, &p.SummaryHTML, &p.PublishedAt,
			&p.ContentHash, &p.APID, &p.AnnouncedAt, &p.UpdatedAt, &p.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}
