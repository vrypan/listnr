package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// schemaVersion1 is the posts table as it existed before deleted_at, used to
// prove an in-place upgrade keeps its rows.
const schemaVersion1Posts = `
CREATE TABLE posts (
	id           INTEGER PRIMARY KEY,
	guid         TEXT NOT NULL UNIQUE,
	url          TEXT NOT NULL,
	title        TEXT NOT NULL DEFAULT '',
	summary_html TEXT NOT NULL DEFAULT '',
	published_at TEXT NOT NULL,
	content_hash TEXT NOT NULL DEFAULT '',
	ap_id        TEXT UNIQUE,
	announced_at TEXT,
	updated_at   TEXT
);
CREATE TABLE schema_migrations (
	version    INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now'))
);
INSERT INTO schema_migrations (version) VALUES (1);
INSERT INTO posts (guid, url, title, published_at, ap_id)
VALUES ('guid-old', 'https://blog.example/old', 'Old', '2026-01-01T00:00:00Z',
        'https://ap.example/posts/old');
`

func TestMigrationAddsDeletedAtToSchemaVersion1(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "listnr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schemaVersion1Posts); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.MigratedFrom() != 1 || st.SchemaVersion() != currentSchemaVersion {
		t.Fatalf("migration %d -> %d, want 1 -> %d",
			st.MigratedFrom(), st.SchemaVersion(), currentSchemaVersion)
	}
	post, err := st.GetPostByGUID("guid-old")
	if err != nil {
		t.Fatal(err)
	}
	if post == nil {
		t.Fatal("pre-existing post lost during migration")
	}
	if post.DeletedAt.Valid {
		t.Fatalf("migrated post deleted_at = %v, want NULL", post.DeletedAt)
	}
	if post.Deleted() {
		t.Fatal("migrated post reports itself deleted")
	}
}

// newDeletionStore returns a store holding one federated post and the given
// follower inboxes, expressed as (personal, shared) pairs.
func newDeletionStore(t *testing.T, followers ...[2]string) (*Store, int64) {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	id, err := st.InsertPost(&Post{
		GUID: "guid-1", URL: "https://blog.example/one", Title: "One",
		PublishedAt: "2026-07-01T00:00:00Z",
		APID:        NullString("https://ap.example/posts/one"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range followers {
		actorID := "https://remote.example/users/" + string(rune('a'+i))
		if err := st.UpsertFollower(actorID, f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	return st, id
}

func countDeliveries(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteFederatedPostFansOutAndIsIdempotent(t *testing.T) {
	st, id := newDeletionStore(t,
		[2]string{"https://a.example/users/a/inbox", "https://a.example/inbox"},
		[2]string{"https://a.example/users/b/inbox", "https://a.example/inbox"},
		[2]string{"https://b.example/users/c/inbox", ""},
	)

	var built int
	build := func(p *Post) ([]byte, error) {
		built++
		if !p.Deleted() {
			t.Fatal("build saw a post without its deletion timestamp")
		}
		return []byte(`{"type":"Delete","published":"` + p.DeletedAt.String + `"}`), nil
	}

	first, err := st.DeleteFederatedPost(id, "2026-07-20T10:00:00Z", build)
	if err != nil {
		t.Fatal(err)
	}
	if first.AlreadyDeleted {
		t.Fatal("first deletion reported already deleted")
	}
	// Two followers share one inbox, so three followers reach two instances.
	if first.Queued != 2 {
		t.Fatalf("queued = %d, want 2 (deduplicated shared inbox)", first.Queued)
	}
	if got := countDeliveries(t, st); got != 2 {
		t.Fatalf("delivery rows = %d, want 2", got)
	}

	second, err := st.DeleteFederatedPost(id, "2026-07-20T11:00:00Z", build)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyDeleted {
		t.Fatal("repeat deletion did not report already deleted")
	}
	if second.Queued != 0 {
		t.Fatalf("repeat queued = %d, want 0", second.Queued)
	}
	if second.Post.DeletedAt.String != "2026-07-20T10:00:00Z" {
		t.Fatalf("repeat moved the timestamp to %q", second.Post.DeletedAt.String)
	}
	if got := countDeliveries(t, st); got != 2 {
		t.Fatalf("delivery rows after repeat = %d, want 2", got)
	}
	if built != 1 {
		t.Fatalf("activity built %d times, want 1", built)
	}
}

func TestDeleteFederatedPostRollsBackWhenBuildFails(t *testing.T) {
	st, id := newDeletionStore(t, [2]string{"https://a.example/inbox", ""})
	boom := errors.New("cannot build activity")
	if _, err := st.DeleteFederatedPost(id, "2026-07-20T10:00:00Z", func(*Post) ([]byte, error) {
		return nil, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	post, err := st.GetPostByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if post.Deleted() {
		t.Fatal("post stayed deleted after the transaction rolled back")
	}
	if got := countDeliveries(t, st); got != 0 {
		t.Fatalf("delivery rows = %d, want 0", got)
	}
}

func TestDeleteFederatedPostRejectsUnknownAndUnfederated(t *testing.T) {
	st, _ := newDeletionStore(t)
	build := func(*Post) ([]byte, error) { return []byte(`{}`), nil }

	if _, err := st.DeleteFederatedPost(9999, "2026-07-20T10:00:00Z", build); !errors.Is(err, ErrPostNotFound) {
		t.Fatalf("unknown id err = %v, want ErrPostNotFound", err)
	}

	unfederated, err := st.InsertPost(&Post{
		GUID: "guid-2", URL: "https://blog.example/two", PublishedAt: "2026-07-02T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteFederatedPost(unfederated, "2026-07-20T10:00:00Z", build); !errors.Is(err, ErrPostNotFound) {
		t.Fatalf("unfederated err = %v, want ErrPostNotFound", err)
	}
}

func TestDeletedPostsLeaveActiveListingsButRemainAddressable(t *testing.T) {
	st, id := newDeletionStore(t)
	before, err := st.PostCount()
	if err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("active post count = %d, want 1", before)
	}

	if _, err := st.DeleteFederatedPost(id, "2026-07-20T10:00:00Z", func(*Post) ([]byte, error) {
		return []byte(`{"type":"Delete"}`), nil
	}); err != nil {
		t.Fatal(err)
	}

	if n, err := st.PostCount(); err != nil || n != 0 {
		t.Fatalf("active post count = %d (err %v), want 0", n, err)
	}
	if n, err := st.TotalPostCount(); err != nil || n != 1 {
		t.Fatalf("total post count = %d (err %v), want 1", n, err)
	}
	active, err := st.ListFederatedPosts(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active listing = %d posts, want 0", len(active))
	}
	admin, err := st.ListPostsForAdmin(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin) != 1 || !admin[0].Deleted() {
		t.Fatalf("admin listing = %+v, want one deleted post", admin)
	}
	// The AP id must still resolve so it can answer with a Tombstone.
	post, err := st.GetPostByAPID("https://ap.example/posts/one")
	if err != nil {
		t.Fatal(err)
	}
	if post == nil || !post.Deleted() {
		t.Fatalf("lookup by AP id = %+v, want the deleted post", post)
	}
}
