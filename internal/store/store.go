package store

import (
	"database/sql"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS posts (
	id           INTEGER PRIMARY KEY,
	guid         TEXT NOT NULL UNIQUE,
	url          TEXT NOT NULL,
	title        TEXT NOT NULL DEFAULT '',
	summary_html TEXT NOT NULL DEFAULT '',
	published_at TEXT NOT NULL,
	content_hash TEXT NOT NULL DEFAULT '',
	ap_id        TEXT UNIQUE,           -- NULL for "seen but never federated"
	announced_at TEXT,
	updated_at   TEXT
);

CREATE TABLE IF NOT EXISTS followers (
	id           INTEGER PRIMARY KEY,
	actor_id     TEXT NOT NULL UNIQUE,
	inbox        TEXT NOT NULL,
	shared_inbox TEXT NOT NULL DEFAULT '',
	followed_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS interactions (
	id             INTEGER PRIMARY KEY,
	ap_id          TEXT NOT NULL UNIQUE,
	kind           TEXT NOT NULL CHECK (kind IN ('reply','like','boost')),
	post_id        INTEGER NOT NULL REFERENCES posts(id),
	actor_id       TEXT NOT NULL,
	actor_handle   TEXT NOT NULL DEFAULT '',
	actor_name     TEXT NOT NULL DEFAULT '',
	actor_icon_url TEXT NOT NULL DEFAULT '',
	content_html   TEXT NOT NULL DEFAULT '',
	published_at   TEXT NOT NULL DEFAULT '',
	received_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
	hidden         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_interactions_post ON interactions(post_id, kind, hidden);

CREATE TABLE IF NOT EXISTS blocks (
	id         INTEGER PRIMARY KEY,
	pattern    TEXT NOT NULL UNIQUE,   -- full actor URL or bare domain
	created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);

CREATE TABLE IF NOT EXISTS deliveries (
	id              INTEGER PRIMARY KEY,
	activity_json   TEXT NOT NULL,
	inbox_url       TEXT NOT NULL,
	attempts        INTEGER NOT NULL DEFAULT 0,
	next_attempt_at TEXT NOT NULL,
	last_error      TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL DEFAULT 'pending'
	                CHECK (status IN ('pending','done','failed'))
);
CREATE INDEX IF NOT EXISTS idx_deliveries_due ON deliveries(status, next_attempt_at);

CREATE TABLE IF NOT EXISTS actor_cache (
	actor_id       TEXT PRIMARY KEY,
	public_key_pem TEXT NOT NULL DEFAULT '',
	name           TEXT NOT NULL DEFAULT '',
	handle         TEXT NOT NULL DEFAULT '',
	icon_url       TEXT NOT NULL DEFAULT '',
	inbox          TEXT NOT NULL DEFAULT '',
	shared_inbox   TEXT NOT NULL DEFAULT '',
	fetched_at     TEXT NOT NULL
);
`

type Store struct {
	DB *sql.DB
}

func Open(dataDir string) (*Store, error) {
	dsn := "file:" + filepath.Join(dataDir, "listnr.db") + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite is happiest with a single writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) FollowerCount() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM followers`).Scan(&n)
	return n, err
}

func (s *Store) PostCount() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM posts WHERE ap_id IS NOT NULL`).Scan(&n)
	return n, err
}
