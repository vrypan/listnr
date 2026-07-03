package cmd

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/config"
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

		apHandler := &ap.Handler{Actor: cfg.Actor, PublicKeyPEM: pubPEM}
		srv := server.New(cfg, st, apHandler, log)

		log.Info("listnr starting",
			"handle", "@"+cfg.Actor.Handle(),
			"actor", cfg.Actor.ID(),
			"listen", cfg.Server.Listen)
		return http.ListenAndServe(cfg.Server.Listen, srv.Routes())
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
