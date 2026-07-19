package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	backupapi "github.com/vrypan/listnr/internal/backup"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/store"
)

func init() {
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export a portable instance backup",
		RunE:  runExport,
	}
	exportCmd.Flags().StringP("output", "o", "", "output file, or - for stdout")
	exportCmd.Flags().Bool("local", false, "export directly from the local instance")

	importCmd := &cobra.Command{
		Use:   "import <archive|->",
		Short: "Restore a backup into a stopped local instance",
		Args:  cobra.ExactArgs(1),
		RunE:  runImport,
	}
	importCmd.Flags().Bool("replace-config", false, "replace an existing config with the archived config")
	rootCmd.AddCommand(exportCmd, importCmd)
}

func runExport(cmd *cobra.Command, _ []string) error {
	output, _ := cmd.Flags().GetString("output")
	local, _ := cmd.Flags().GetBool("local")
	if output == "" {
		output = "listnr-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
	}
	if output == "-" && isTerminal(cmd.OutOrStdout()) {
		return errors.New("refusing to write a binary backup to a terminal")
	}

	tmp, err := createExportTemp(output)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	if local {
		err = writeLocalExport(cmd.Context(), tmp)
	} else {
		err = downloadExport(cmd, tmp)
	}
	if err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	validated, err := backupapi.Validate(cmd.Context(), tmp)
	if err != nil {
		return fmt.Errorf("validate exported archive: %w", err)
	}
	manifest := validated.Manifest
	validated.Close()
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "warning: backup contains an unencrypted private key and admin token")
	if output == "-" {
		_, err = io.Copy(cmd.OutOrStdout(), tmp)
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, output); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "exported %s (actor %s, schema %d)\n",
		output, manifest.ActorHandle, manifest.SchemaVersion)
	return nil
}

func createExportTemp(output string) (*os.File, error) {
	if output == "-" {
		return os.CreateTemp("", "listnr-export-*.tar.gz")
	}
	if _, err := os.Stat(output); err == nil {
		return nil, fmt.Errorf("output file already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	dir := filepath.Dir(output)
	return os.CreateTemp(dir, ".listnr-export-*.tar.gz")
}

func writeLocalExport(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Server.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = backupapi.Write(ctx, w, backupapi.Source{
		Store: st, DataDir: cfg.Server.DataDir, ConfigPath: configPath, Actor: cfg.Actor,
	})
	return err
}

func downloadExport(cmd *cobra.Command, w io.Writer) error {
	cfg, err := loadCLIConfig()
	if err != nil {
		return err
	}
	path := "/admin/export"
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost,
		strings.TrimRight(cfg.Server, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return readErr
		}
		return fmt.Errorf("POST %s: HTTP %d: %s", path, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func runImport(cmd *cobra.Command, args []string) error {
	replaceConfig, _ := cmd.Flags().GetBool("replace-config")
	var r io.Reader
	var f *os.File
	if args[0] == "-" {
		if isTerminal(cmd.InOrStdin()) {
			return errors.New("refusing to read a binary backup from a terminal")
		}
		r = cmd.InOrStdin()
	} else {
		var err error
		f, err = os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	result, err := backupapi.Import(cmd.Context(), r, backupapi.ImportOptions{
		ConfigPath: configPath, ReplaceConfig: replaceConfig,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "restored actor %s (schema %d) to %s\n",
		result.Manifest.ActorHandle, result.Manifest.SchemaVersion, result.DataDir)
	fmt.Fprintf(cmd.OutOrStdout(), "previous files retained in %s\n", result.RollbackDir)
	if result.ConfigRestored {
		fmt.Fprintf(cmd.OutOrStdout(), "restored config to %s\n", configPath)
	}
	return nil
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
