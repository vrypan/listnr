package store

import (
	"net/url"
	"strings"
)

type Block struct {
	ID        int64  `json:"id"`
	Pattern   string `json:"pattern"`
	CreatedAt string `json:"created_at"`
}

func (s *Store) AddBlock(pattern string) error {
	if _, err := s.DB.Exec(`INSERT OR IGNORE INTO blocks (pattern) VALUES (?)`, pattern); err != nil {
		return err
	}
	rows, err := s.DB.Query(`SELECT DISTINCT actor_id FROM interactions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var actorID string
		if err := rows.Scan(&actorID); err != nil {
			return err
		}
		if BlockMatches(pattern, actorID) {
			matches = append(matches, actorID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, actorID := range matches {
		if _, err := s.DB.Exec(`UPDATE interactions SET hidden = 1 WHERE actor_id = ?`, actorID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteBlock(pattern string) error {
	_, err := s.DB.Exec(`DELETE FROM blocks WHERE pattern = ? OR id = ?`, pattern, pattern)
	return err
}

func (s *Store) ListBlocks() ([]Block, error) {
	rows, err := s.DB.Query(`SELECT id, pattern, created_at FROM blocks ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blocks []Block
	for rows.Next() {
		var b Block
		if err := rows.Scan(&b.ID, &b.Pattern, &b.CreatedAt); err != nil {
			return nil, err
		}
		blocks = append(blocks, b)
	}
	return blocks, rows.Err()
}

// IsBlocked reports whether actorID matches any block pattern. A pattern is
// either a full actor URL (exact match) or a bare domain, which matches the
// actor's host and any subdomain of it.
func (s *Store) IsBlocked(actorID string) (bool, error) {
	rows, err := s.DB.Query(`SELECT pattern FROM blocks`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			return false, err
		}
		if BlockMatches(pattern, actorID) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// BlockMatches reports whether a single block pattern matches an actor id.
func BlockMatches(pattern, actorID string) bool {
	if pattern == actorID {
		return true
	}
	u, err := url.Parse(actorID)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	pattern = strings.ToLower(pattern)
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}
