package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/instance"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

func TestWriteValidateAndImport(t *testing.T) {
	sourceDir := t.TempDir()
	sourceConfig := writeTestConfig(t, filepath.Join(t.TempDir(), "listnr.toml"), sourceDir,
		"ap.vrypan.net")
	cfg, err := config.Load(sourceConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.LoadOrCreate(sourceDir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.InsertPost(&store.Post{
		GUID: "post-1", URL: "https://blog.vrypan.net/post-1", Title: "Post",
		PublishedAt: "2026-07-19T00:00:00Z", APID: store.NullString("https://ap.vrypan.net/posts/1"),
	}); err != nil {
		t.Fatal(err)
	}

	var archive bytes.Buffer
	manifest, err := Write(context.Background(), &archive, Source{
		Store: st, DataDir: sourceDir, ConfigPath: sourceConfig, Actor: cfg.Actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ActorID != "https://ap.vrypan.net/actor" || manifest.SchemaVersion != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	validated, err := Validate(context.Background(), bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	validated.Close()

	destinationDir := t.TempDir()
	destinationConfig := writeTestConfig(t, filepath.Join(t.TempDir(), "listnr.toml"),
		destinationDir, "ap.vrypan.net")
	oldKey, err := keys.LoadOrCreate(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint := keys.Fingerprint(oldKey)
	destinationStore, err := store.Open(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := destinationStore.InsertPost(&store.Post{
		GUID: "old", URL: "https://blog.vrypan.net/old", PublishedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	destinationStore.Close()

	result, err := Import(context.Background(), bytes.NewReader(archive.Bytes()), ImportOptions{
		ConfigPath: destinationConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ConfigRestored {
		t.Fatal("Import() replaced an existing config without --replace-config")
	}
	if _, err := os.Stat(filepath.Join(result.RollbackDir, "listnr.db")); err != nil {
		t.Fatalf("rollback database: %v", err)
	}
	restored, err := store.Open(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if post, err := restored.GetPostByGUID("post-1"); err != nil || post == nil {
		t.Fatalf("restored post = %+v, %v", post, err)
	}
	keyBytes, err := os.ReadFile(filepath.Join(destinationDir, "actor.pem"))
	if err != nil {
		t.Fatal(err)
	}
	restoredKey, err := keys.ParsePrivatePEM(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys.Fingerprint(restoredKey); got == oldFingerprint || got != manifest.PublicKeyFingerprint {
		t.Fatalf("restored key fingerprint = %s", got)
	}
}

func TestImportRejectsDifferentActorAndActiveDaemon(t *testing.T) {
	archive := testArchive(t)
	destinationDir := t.TempDir()
	configPath := writeTestConfig(t, filepath.Join(t.TempDir(), "listnr.toml"),
		destinationDir, "other.example")
	if _, err := Import(context.Background(), bytes.NewReader(archive), ImportOptions{
		ConfigPath: configPath,
	}); err == nil || !strings.Contains(err.Error(), "does not match backup actor") {
		t.Fatalf("actor mismatch error = %v", err)
	}

	configPath = writeTestConfig(t, filepath.Join(t.TempDir(), "listnr.toml"),
		destinationDir, "ap.vrypan.net")
	lock, err := instance.Acquire(destinationDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := Import(context.Background(), bytes.NewReader(archive), ImportOptions{
		ConfigPath: configPath,
	}); !strings.Contains(fmt.Sprint(err), "in use") {
		t.Fatalf("locked import error = %v", err)
	}
}

func TestValidateRejectsUnexpectedArchivePath(t *testing.T) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../actor.pem", Mode: 0o600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	if _, err := Validate(context.Background(), &b); err == nil ||
		!strings.Contains(err.Error(), "unexpected archive entry") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsChecksumMismatch(t *testing.T) {
	archive := testArchive(t)
	var output bytes.Buffer
	inGzip, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	inTar := tar.NewReader(inGzip)
	outGzip := gzip.NewWriter(&output)
	outTar := tar.NewWriter(outGzip)
	for {
		header, err := inTar.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
		data, err := io.ReadAll(inTar)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == configName {
			data = append(data, '#')
			header.Size = int64(len(data))
		}
		if err := outTar.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := outTar.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := outTar.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outGzip.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(context.Background(), &output); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func testArchive(t *testing.T) []byte {
	t.Helper()
	dataDir := t.TempDir()
	configPath := writeTestConfig(t, filepath.Join(t.TempDir(), "listnr.toml"), dataDir,
		"ap.vrypan.net")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keys.LoadOrCreate(dataDir); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var b bytes.Buffer
	if _, err := Write(context.Background(), &b, Source{
		Store: st, DataDir: dataDir, ConfigPath: configPath, Actor: cfg.Actor,
	}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeTestConfig(t *testing.T, path, dataDir, host string) string {
	t.Helper()
	contents := fmt.Sprintf(`[actor]
username = "blog"
domain = "vrypan.net"
host = %q
blog_url = "https://blog.vrypan.net"

[feed]
url = "https://blog.vrypan.net/feed.xml"

[server]
data_dir = %q

[admin]
token = "secret"
`, host, dataDir)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
