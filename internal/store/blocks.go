package store

import (
	"net/url"
	"strings"
)

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
