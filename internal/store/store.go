package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"modernc.org/sqlite"
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
	in_reply_to    TEXT NOT NULL DEFAULT '',
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

CREATE TABLE IF NOT EXISTS state (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS seen_activities (
	activity_id TEXT PRIMARY KEY,
	seen_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_seen_activities_seen ON seen_activities(seen_at);

CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
`

const currentSchemaVersion = 1

type Store struct {
	DB            *sql.DB
	schemaVersion int
	migratedFrom  int
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
	from, to, err := migrate(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db, schemaVersion: to, migratedFrom: from}, nil
}

type migration struct {
	version int
	apply   func(*sql.Tx) error
}

var migrations = []migration{
	{
		version: 1,
		apply: func(tx *sql.Tx) error {
			exists, err := columnExists(tx, "interactions", "in_reply_to")
			if err != nil || exists {
				return err
			}
			_, err = tx.Exec(`ALTER TABLE interactions ADD COLUMN in_reply_to TEXT NOT NULL DEFAULT ''`)
			return err
		},
	},
}

// migrate applies each missing migration in its own transaction. Returning
// both versions lets the daemon report upgrades in its startup log.
func migrate(db *sql.DB) (int, int, error) {
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, 0, err
	}
	from := version
	if version > currentSchemaVersion {
		return from, version, fmt.Errorf(
			"database schema version %d is newer than supported version %d",
			version, currentSchemaVersion)
	}
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return from, version, err
		}
		if err := m.apply(tx); err != nil {
			tx.Rollback()
			return from, version, fmt.Errorf("apply schema migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
			tx.Rollback()
			return from, version, err
		}
		if err := tx.Commit(); err != nil {
			return from, version, err
		}
		version = m.version
	}
	if version != currentSchemaVersion {
		return from, version, fmt.Errorf(
			"database schema at version %d, expected %d",
			version, currentSchemaVersion)
	}
	return from, version, nil
}

func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?) WHERE name = ?`, table, column)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) SchemaVersion() int { return s.schemaVersion }

func (s *Store) MigratedFrom() int { return s.migratedFrom }

func CurrentSchemaVersion() int { return currentSchemaVersion }

// BackupTo creates a consistent standalone SQLite snapshot, including data
// that has not yet been checkpointed from the source database's WAL.
func (s *Store) BackupTo(ctx context.Context, destination string) error {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite driver connection %T does not support online backup", driverConn)
		}
		backup, err := backuper.NewBackup(destination)
		if err != nil {
			return err
		}
		more, stepErr := backup.Step(-1)
		if stepErr == nil && more {
			stepErr = fmt.Errorf("sqlite backup did not finish")
		}
		finishedConn, finishErr := backup.Commit()
		if finishedConn != nil {
			finishErr = errors.Join(finishErr, finishedConn.Close())
		}
		return errors.Join(stepErr, finishErr)
	})
}

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
