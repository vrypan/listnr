package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var configPath string

var rootCmd = &cobra.Command{
	Use:   "listnr",
	Short: "ActivityPub bridge for a static blog",
	Long: `listnr gives a statically generated blog a fediverse presence:
it serves a single ActivityPub actor, announces new posts from the blog's
RSS/Atom feed to followers, and collects replies/likes/boosts so the blog
can display them as comments.`,
	SilenceUsage: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "listnr.toml", "path to config file")
}
