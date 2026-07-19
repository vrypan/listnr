package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/instance"
)

type ImportOptions struct {
	ConfigPath    string
	ReplaceConfig bool
}

type ImportResult struct {
	Manifest       Manifest
	DataDir        string
	RollbackDir    string
	ConfigRestored bool
}

// Import validates and restores an archive into a stopped listnr instance.
// Existing runtime files are retained in a rollback directory.
func Import(ctx context.Context, r io.Reader, opts ImportOptions) (*ImportResult, error) {
	validated, err := Validate(ctx, r)
	if err != nil {
		return nil, err
	}
	defer validated.Close()

	destinationConfig, restoreConfig, err := selectDestinationConfig(validated, opts)
	if err != nil {
		return nil, err
	}
	if destinationConfig.Actor.ID() != validated.Manifest.ActorID ||
		destinationConfig.Actor.Handle() != validated.Manifest.ActorHandle {
		return nil, fmt.Errorf(
			"destination actor %s (%s) does not match backup actor %s (%s)",
			destinationConfig.Actor.ID(), destinationConfig.Actor.Handle(),
			validated.Manifest.ActorID, validated.Manifest.ActorHandle)
	}

	dataDir := destinationConfig.Server.DataDir
	lock, err := instance.Acquire(dataDir)
	if err != nil {
		return nil, err
	}
	defer lock.Close()

	stagingDir, err := os.MkdirTemp(dataDir, ".listnr-import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stagingDir)
	stagedDB := filepath.Join(stagingDir, "listnr.db")
	stagedKey := filepath.Join(stagingDir, "actor.pem")
	if err := copyFile(validated.DatabasePath, stagedDB, 0o600); err != nil {
		return nil, fmt.Errorf("stage database: %w", err)
	}
	if err := copyFile(validated.KeyPath, stagedKey, 0o600); err != nil {
		return nil, fmt.Errorf("stage actor key: %w", err)
	}
	if err := chownLike(dataDir, stagedDB, stagedKey); err != nil {
		return nil, err
	}

	rollbackDir, err := os.MkdirTemp(dataDir, "pre-import-"+
		time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(rollbackDir, 0o700); err != nil {
		os.Remove(rollbackDir)
		return nil, err
	}
	if err := chownLike(dataDir, rollbackDir); err != nil {
		os.Remove(rollbackDir)
		return nil, err
	}

	moved := make([]string, 0, 4)
	rollback := func(cause error) error {
		var rollbackErr error
		for _, name := range []string{"listnr.db", "listnr.db-wal", "listnr.db-shm", "actor.pem"} {
			live := filepath.Join(dataDir, name)
			old := filepath.Join(rollbackDir, name)
			if contains(moved, name) {
				rollbackErr = errors.Join(rollbackErr, removeIfExists(live), os.Rename(old, live))
			} else if name == "listnr.db" || name == "actor.pem" {
				rollbackErr = errors.Join(rollbackErr, removeIfExists(live))
			}
		}
		return errors.Join(cause, rollbackErr)
	}

	for _, name := range []string{"listnr.db", "listnr.db-wal", "listnr.db-shm", "actor.pem"} {
		live := filepath.Join(dataDir, name)
		if _, err := os.Lstat(live); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, rollback(err)
		}
		if err := os.Rename(live, filepath.Join(rollbackDir, name)); err != nil {
			return nil, rollback(err)
		}
		moved = append(moved, name)
	}
	if err := os.Rename(stagedDB, filepath.Join(dataDir, "listnr.db")); err != nil {
		return nil, rollback(err)
	}
	if err := os.Rename(stagedKey, filepath.Join(dataDir, "actor.pem")); err != nil {
		return nil, rollback(err)
	}

	if restoreConfig {
		configBytes, err := os.ReadFile(validated.ConfigPath)
		if err != nil {
			return nil, rollback(err)
		}
		if err := preserveConfig(opts.ConfigPath, rollbackDir); err != nil {
			return nil, rollback(err)
		}
		if err := atomicWriteFile(opts.ConfigPath, configBytes, 0o600); err != nil {
			return nil, rollback(err)
		}
	}

	return &ImportResult{
		Manifest:       validated.Manifest,
		DataDir:        dataDir,
		RollbackDir:    rollbackDir,
		ConfigRestored: restoreConfig,
	}, nil
}

func selectDestinationConfig(v *Validated, opts ImportOptions) (*config.Config, bool, error) {
	if opts.ConfigPath == "" {
		return nil, false, errors.New("config path is required")
	}
	_, err := os.Stat(opts.ConfigPath)
	switch {
	case err == nil && !opts.ReplaceConfig:
		cfg, err := config.Load(opts.ConfigPath)
		return cfg, false, err
	case err == nil && opts.ReplaceConfig:
		current, err := config.Load(opts.ConfigPath)
		if err != nil {
			return nil, false, err
		}
		if current.Actor.ID() != v.Manifest.ActorID ||
			current.Actor.Handle() != v.Manifest.ActorHandle {
			return nil, false, errors.New("existing config belongs to a different actor")
		}
		return v.Config, true, nil
	case errors.Is(err, os.ErrNotExist):
		return v.Config, true, nil
	default:
		return nil, false, err
	}
}

func preserveConfig(path, rollbackDir string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return copyFile(path, filepath.Join(rollbackDir, "listnr.toml"), info.Mode().Perm())
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	var owner *syscall.Stat_t
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		owner, _ = info.Sys().(*syscall.Stat_t)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".listnr-config-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if owner != nil {
		if err := tmp.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

func chownLike(reference string, paths ...string) error {
	info, err := os.Stat(reference)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	for _, path := range paths {
		if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
