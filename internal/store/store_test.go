package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSchemaMigrationsAreVersionedAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "listnr.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE interactions (
			id INTEGER PRIMARY KEY,
			ap_id TEXT NOT NULL UNIQUE,
			kind TEXT NOT NULL,
			post_id INTEGER NOT NULL,
			actor_id TEXT NOT NULL,
			actor_handle TEXT NOT NULL DEFAULT '',
			actor_name TEXT NOT NULL DEFAULT '',
			actor_icon_url TEXT NOT NULL DEFAULT '',
			content_html TEXT NOT NULL DEFAULT '',
			published_at TEXT NOT NULL DEFAULT '',
			received_at TEXT NOT NULL DEFAULT '',
			hidden INTEGER NOT NULL DEFAULT 0
		)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.MigratedFrom() != 0 || st.SchemaVersion() != currentSchemaVersion {
		t.Fatalf("migration %d -> %d, want 0 -> %d", st.MigratedFrom(), st.SchemaVersion(), currentSchemaVersion)
	}
	var columns int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('interactions') WHERE name = 'in_reply_to'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 1 {
		t.Fatalf("in_reply_to columns = %d, want 1", columns)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.MigratedFrom() != currentSchemaVersion || st.SchemaVersion() != currentSchemaVersion {
		t.Fatalf("reopen migration %d -> %d, want %d -> %d",
			st.MigratedFrom(), st.SchemaVersion(), currentSchemaVersion, currentSchemaVersion)
	}
}
