package store

import (
	"database/sql"
	"errors"
)

func (s *Store) GetState(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetState(key, value string) error {
	_, err := s.DB.Exec(`
		INSERT INTO state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
