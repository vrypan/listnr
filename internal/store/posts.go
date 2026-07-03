package store

import (
	"database/sql"
	"errors"
	"time"
)

type Post struct {
	ID          int64
	GUID        string
	URL         string
	Title       string
	SummaryHTML string
	PublishedAt string
	ContentHash string
	APID        sql.NullString
	AnnouncedAt sql.NullString
	UpdatedAt   sql.NullString
}

func (s *Store) TotalPostCount() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&n)
	return n, err
}

func (s *Store) GetPostByGUID(guid string) (*Post, error) {
	return s.getPost(`SELECT id, guid, url, title, summary_html, published_at, content_hash, ap_id, announced_at, updated_at FROM posts WHERE guid = ?`, guid)
}

func (s *Store) GetPostByAPID(apID string) (*Post, error) {
	return s.getPost(`SELECT id, guid, url, title, summary_html, published_at, content_hash, ap_id, announced_at, updated_at FROM posts WHERE ap_id = ?`, apID)
}

func (s *Store) GetPostByURL(url string) (*Post, error) {
	return s.getPost(`SELECT id, guid, url, title, summary_html, published_at, content_hash, ap_id, announced_at, updated_at FROM posts WHERE url = ?`, url)
}

func (s *Store) getPost(query string, args ...any) (*Post, error) {
	var p Post
	err := s.DB.QueryRow(query, args...).Scan(&p.ID, &p.GUID, &p.URL, &p.Title,
		&p.SummaryHTML, &p.PublishedAt, &p.ContentHash, &p.APID, &p.AnnouncedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) InsertPost(p *Post) (int64, error) {
	res, err := s.DB.Exec(`
		INSERT INTO posts (guid, url, title, summary_html, published_at, content_hash, ap_id, announced_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.GUID, p.URL, p.Title, p.SummaryHTML, p.PublishedAt, p.ContentHash,
		nullStringArg(p.APID), nullStringArg(p.AnnouncedAt), nullStringArg(p.UpdatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePostContent(guid, title, summaryHTML, contentHash, updatedAt string) error {
	_, err := s.DB.Exec(`
		UPDATE posts
		SET title = ?, summary_html = ?, content_hash = ?, updated_at = ?
		WHERE guid = ?`,
		title, summaryHTML, contentHash, updatedAt, guid)
	return err
}

func (s *Store) ListFederatedPosts(limit, offset int) ([]Post, error) {
	rows, err := s.DB.Query(`
		SELECT id, guid, url, title, summary_html, published_at, content_hash, ap_id, announced_at, updated_at
		FROM posts
		WHERE ap_id IS NOT NULL
		ORDER BY published_at DESC, id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		var p Post
		if err := rows.Scan(&p.ID, &p.GUID, &p.URL, &p.Title, &p.SummaryHTML,
			&p.PublishedAt, &p.ContentHash, &p.APID, &p.AnnouncedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	return posts, rows.Err()
}

func nullStringArg(ns sql.NullString) any {
	if ns.Valid {
		return ns.String
	}
	return nil
}

func NullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func NowString() string { return time.Now().UTC().Format(time.RFC3339) }
