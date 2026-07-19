// Package backup creates and validates portable listnr instance archives.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vrypan/listnr/internal/buildinfo"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/store"
)

const (
	FormatVersion = 1
	manifestName  = "manifest.json"
	databaseName  = "data/listnr.db"
	keyName       = "data/actor.pem"
	configName    = "config/listnr.toml"
	maxSmallFile  = 1 << 20
	maxDatabase   = 100 << 30
)

type FileRecord struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	FormatVersion        int                   `json:"format_version"`
	CreatedAt            string                `json:"created_at"`
	Build                buildinfo.Details     `json:"build"`
	SchemaVersion        int                   `json:"schema_version"`
	ActorID              string                `json:"actor_id"`
	ActorHandle          string                `json:"actor_handle"`
	PublicKeyFingerprint string                `json:"public_key_fingerprint"`
	Files                map[string]FileRecord `json:"files"`
}

type Source struct {
	Store      *store.Store
	DataDir    string
	ConfigPath string
	Actor      config.Actor
}

type Validated struct {
	Dir          string
	Manifest     Manifest
	Config       *config.Config
	DatabasePath string
	KeyPath      string
	ConfigPath   string
}

func (v *Validated) Close() error {
	if v == nil || v.Dir == "" {
		return nil
	}
	err := os.RemoveAll(v.Dir)
	v.Dir = ""
	return err
}

func Write(ctx context.Context, w io.Writer, src Source) (*Manifest, error) {
	if src.Store == nil {
		return nil, errors.New("backup source has no store")
	}
	tmpDir, err := os.MkdirTemp("", "listnr-export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	databasePath := filepath.Join(tmpDir, "listnr.db")
	if err := src.Store.BackupTo(ctx, databasePath); err != nil {
		return nil, fmt.Errorf("snapshot database: %w", err)
	}
	keyPath := filepath.Join(src.DataDir, "actor.pem")
	configPath := src.ConfigPath

	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read actor key: %w", err)
	}
	privateKey, err := keys.ParsePrivatePEM(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse actor key: %w", err)
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	manifest := &Manifest{
		FormatVersion:        FormatVersion,
		CreatedAt:            time.Now().UTC().Format(time.RFC3339),
		Build:                buildinfo.Current(),
		SchemaVersion:        src.Store.SchemaVersion(),
		ActorID:              src.Actor.ID(),
		ActorHandle:          src.Actor.Handle(),
		PublicKeyFingerprint: keys.Fingerprint(privateKey),
		Files:                make(map[string]FileRecord, 3),
	}
	manifest.Files[databaseName], err = hashFile(databasePath)
	if err != nil {
		return nil, err
	}
	manifest.Files[keyName] = hashBytes(keyBytes)
	manifest.Files[configName] = hashBytes(configBytes)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestJSON = append(manifestJSON, '\n')

	gz := gzip.NewWriter(w)
	gz.Header.ModTime = time.Time{}
	tw := tar.NewWriter(gz)
	createdAt, _ := time.Parse(time.RFC3339, manifest.CreatedAt)
	if err := writeBytes(tw, manifestName, manifestJSON, createdAt); err != nil {
		return nil, closeWriters(tw, gz, err)
	}
	if err := writeFile(tw, databaseName, databasePath, createdAt); err != nil {
		return nil, closeWriters(tw, gz, err)
	}
	if err := writeBytes(tw, keyName, keyBytes, createdAt); err != nil {
		return nil, closeWriters(tw, gz, err)
	}
	if err := writeBytes(tw, configName, configBytes, createdAt); err != nil {
		return nil, closeWriters(tw, gz, err)
	}
	if err := closeWriters(tw, gz, nil); err != nil {
		return nil, err
	}
	return manifest, nil
}

func Validate(ctx context.Context, r io.Reader) (*Validated, error) {
	dir, err := os.MkdirTemp("", "listnr-import-")
	if err != nil {
		return nil, err
	}
	v := &Validated{
		Dir:          dir,
		DatabasePath: filepath.Join(dir, "listnr.db"),
		KeyPath:      filepath.Join(dir, "actor.pem"),
		ConfigPath:   filepath.Join(dir, "listnr.toml"),
	}
	fail := func(err error) (*Validated, error) {
		v.Close()
		return nil, err
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return fail(fmt.Errorf("open gzip stream: %w", err))
	}
	defer gz.Close()
	tw := tar.NewReader(gz)
	outputs := map[string]string{
		databaseName: v.DatabasePath,
		keyName:      v.KeyPath,
		configName:   v.ConfigPath,
	}
	seen := map[string]bool{}
	var manifestJSON []byte
	for {
		hdr, err := tw.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(fmt.Errorf("read archive: %w", err))
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			return fail(fmt.Errorf("archive entry %q is not a regular file", hdr.Name))
		}
		if seen[hdr.Name] {
			return fail(fmt.Errorf("duplicate archive entry %q", hdr.Name))
		}
		seen[hdr.Name] = true
		switch hdr.Name {
		case manifestName:
			if hdr.Size < 0 || hdr.Size > maxSmallFile {
				return fail(errors.New("manifest is too large"))
			}
			manifestJSON, err = io.ReadAll(io.LimitReader(tw, hdr.Size))
			if err != nil || int64(len(manifestJSON)) != hdr.Size {
				return fail(fmt.Errorf("read manifest: %w", errors.Join(err, io.ErrUnexpectedEOF)))
			}
		case databaseName, keyName, configName:
			limit := int64(maxSmallFile)
			if hdr.Name == databaseName {
				limit = maxDatabase
			}
			if hdr.Size < 0 || hdr.Size > limit {
				return fail(fmt.Errorf("archive entry %q exceeds size limit", hdr.Name))
			}
			if err := extractFile(tw, outputs[hdr.Name], hdr.Size); err != nil {
				return fail(err)
			}
		default:
			return fail(fmt.Errorf("unexpected archive entry %q", hdr.Name))
		}
	}
	for _, name := range []string{manifestName, databaseName, keyName, configName} {
		if !seen[name] {
			return fail(fmt.Errorf("archive is missing %q", name))
		}
	}
	if err := json.Unmarshal(manifestJSON, &v.Manifest); err != nil {
		return fail(fmt.Errorf("parse manifest: %w", err))
	}
	if err := validateExtracted(ctx, v); err != nil {
		return fail(err)
	}
	return v, nil
}

func validateExtracted(ctx context.Context, v *Validated) error {
	if v.Manifest.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported export format %d", v.Manifest.FormatVersion)
	}
	paths := map[string]string{
		databaseName: v.DatabasePath,
		keyName:      v.KeyPath,
		configName:   v.ConfigPath,
	}
	if len(v.Manifest.Files) != len(paths) {
		return errors.New("manifest has an unexpected file list")
	}
	for name, path := range paths {
		want, ok := v.Manifest.Files[name]
		if !ok {
			return fmt.Errorf("manifest is missing checksum for %q", name)
		}
		got, err := hashFile(path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("checksum mismatch for %q", name)
		}
	}
	keyBytes, err := os.ReadFile(v.KeyPath)
	if err != nil {
		return err
	}
	privateKey, err := keys.ParsePrivatePEM(keyBytes)
	if err != nil {
		return fmt.Errorf("parse actor key: %w", err)
	}
	if got := keys.Fingerprint(privateKey); got != v.Manifest.PublicKeyFingerprint {
		return errors.New("actor key fingerprint does not match manifest")
	}
	archivedConfig, err := config.Load(v.ConfigPath)
	if err != nil {
		return fmt.Errorf("parse archived config: %w", err)
	}
	if archivedConfig.Actor.ID() != v.Manifest.ActorID ||
		archivedConfig.Actor.Handle() != v.Manifest.ActorHandle {
		return errors.New("archived config actor does not match manifest")
	}
	v.Config = archivedConfig

	schemaVersion, err := validateDatabase(ctx, v.DatabasePath)
	if err != nil {
		return err
	}
	if schemaVersion != v.Manifest.SchemaVersion {
		return fmt.Errorf("database schema version %d does not match manifest version %d",
			schemaVersion, v.Manifest.SchemaVersion)
	}
	if schemaVersion > store.CurrentSchemaVersion() {
		return fmt.Errorf("database schema version %d is newer than supported version %d",
			schemaVersion, store.CurrentSchemaVersion())
	}
	return nil
}

func validateDatabase(ctx context.Context, path string) (int, error) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return 0, fmt.Errorf("check database integrity: %w", err)
	}
	if integrity != "ok" {
		return 0, fmt.Errorf("database integrity check failed: %s", integrity)
	}
	var version int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read database schema version: %w", err)
	}
	return version, nil
}

func hashFile(path string) (FileRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileRecord{}, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return FileRecord{}, err
	}
	return FileRecord{SHA256: hex.EncodeToString(h.Sum(nil)), Size: size}, nil
}

func hashBytes(data []byte) FileRecord {
	sum := sha256.Sum256(data)
	return FileRecord{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func extractFile(r io.Reader, path string, size int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(f, r, size)
	closeErr := f.Close()
	if copyErr != nil || written != size {
		return errors.Join(copyErr, closeErr, io.ErrUnexpectedEOF)
	}
	return closeErr
}

func writeBytes(tw *tar.Writer, name string, data []byte, modTime time.Time) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: modTime,
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func writeFile(tw *tar.Writer, name, path string, modTime time.Time) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: info.Size(), ModTime: modTime,
	}); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func closeWriters(tw *tar.Writer, gz *gzip.Writer, prior error) error {
	return errors.Join(prior, tw.Close(), gz.Close())
}
