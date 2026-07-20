package cmd

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/buildinfo"
	"github.com/vrypan/listnr/internal/config"
	"github.com/vrypan/listnr/internal/delivery"
	"github.com/vrypan/listnr/internal/fedi"
	"github.com/vrypan/listnr/internal/feed"
	"github.com/vrypan/listnr/internal/instance"
	"github.com/vrypan/listnr/internal/keys"
	"github.com/vrypan/listnr/internal/server"
	"github.com/vrypan/listnr/internal/store"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the listnr daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		lock, err := instance.Acquire(cfg.Server.DataDir)
		if err != nil {
			return err
		}
		defer lock.Close()
		key, err := keys.LoadOrCreate(cfg.Server.DataDir)
		if err != nil {
			return err
		}
		pubPEM, err := keys.PublicPEM(key)
		if err != nil {
			return err
		}
		st, err := store.Open(cfg.Server.DataDir)
		if err != nil {
			return err
		}
		defer st.Close()
		if st.MigratedFrom() != st.SchemaVersion() {
			log.Info("database migrated", "from", st.MigratedFrom(), "to", st.SchemaVersion())
		}

		keyID := cfg.Actor.ID() + "#main-key"
		fetcher := fedi.NewClient(st, key, keyID, nil)
		queue := delivery.NewQueue(st, key, keyID, log, nil)

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go queue.Run(ctx)

		apHandler := &ap.Handler{Actor: cfg.Actor, PublicKeyPEM: pubPEM}
		srv := server.New(cfg, st, apHandler, fetcher, queue, log)
		srv.SetConfigPath(configPath)
		// A migration published in an earlier run must keep showing in the
		// actor document across restarts.
		if err := srv.RestoreMoveState(); err != nil {
			return err
		}
		poller := feed.NewPoller(cfg, st, queue, log)
		srv.SetPollFunc(poller.Trigger)
		go poller.Run(ctx)
		httpSrv := &http.Server{
			Addr:              cfg.Server.Listen,
			Handler:           srv.Routes(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}

		build := buildinfo.Current()
		log.Info("listnr starting",
			"version", build.Version,
			"commit", build.Commit,
			"schema_version", st.SchemaVersion(),
			"handle", "@"+cfg.Actor.Handle(),
			"actor", cfg.Actor.ID(),
			"listen", cfg.Server.Listen)
		errc := make(chan error, 1)
		go func() {
			errc <- httpSrv.ListenAndServe()
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			cancel()
			return httpSrv.Shutdown(shutdownCtx)
		case err := <-errc:
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
