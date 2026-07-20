package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

var cliServer, cliToken string

type cliConfig struct {
	Server string `toml:"server"`
	Token  string `toml:"token"`
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cliServer, "server", "", "admin API server URL")
	rootCmd.PersistentFlags().StringVar(&cliToken, "token", "", "admin API bearer token")

	replies := &cobra.Command{Use: "replies", Short: "Manage stored replies"}
	repliesList := &cobra.Command{Use: "list", RunE: runRepliesList}
	repliesList.Flags().String("post", "", "filter by post URL")
	repliesList.Flags().Bool("hidden", false, "show only hidden replies")
	replies.AddCommand(repliesList)
	replies.AddCommand(replyAction("hide"), replyAction("unhide"), replyAction("delete"))

	block := &cobra.Command{Use: "block", Short: "Manage blocks"}
	block.AddCommand(&cobra.Command{Use: "list", RunE: runBlockList})
	block.AddCommand(&cobra.Command{Use: "add <pattern>", Args: cobra.ExactArgs(1), RunE: runBlockAdd})
	block.AddCommand(&cobra.Command{Use: "rm <pattern>", Args: cobra.ExactArgs(1), RunE: runBlockRemove})

	followers := &cobra.Command{Use: "followers", Short: "Manage followers"}
	followers.AddCommand(&cobra.Command{Use: "list", RunE: runFollowersList})
	followers.AddCommand(&cobra.Command{Use: "rm <id>", Args: cobra.ExactArgs(1), RunE: runFollowerRemove})

	posts := &cobra.Command{Use: "posts", Short: "Manage published posts"}
	postsList := &cobra.Command{Use: "list", RunE: runPostsList}
	postsList.Flags().Int("limit", 0, "maximum posts to show (server caps at 200)")
	postsList.Flags().Int("offset", 0, "skip this many posts")
	posts.AddCommand(postsList)
	posts.AddCommand(&cobra.Command{
		Use:   "delete <id>",
		Short: "Withdraw a post and send a Delete to followers",
		Args:  cobra.ExactArgs(1),
		RunE:  runPostDelete,
	})

	actor := &cobra.Command{Use: "actor", Short: "Manage the local actor"}
	actor.AddCommand(&cobra.Command{
		Use:   "publish",
		Short: "Announce the daemon's current actor profile to followers",
		RunE:  runActorPublish,
	})

	rootCmd.AddCommand(replies, block, followers, posts, actor)
	rootCmd.AddCommand(&cobra.Command{Use: "stats", RunE: runStats})
	rootCmd.AddCommand(&cobra.Command{
		Use:     "refresh",
		Aliases: []string{"poll"},
		Short:   "Tell the running server to fetch the RSS feed now",
		RunE:    runPoll,
	})
}

func replyAction(action string) *cobra.Command {
	return &cobra.Command{
		Use:  action + " <id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := http.MethodPost
			path := "/admin/replies/" + args[0] + "/" + action
			if action == "delete" {
				method = http.MethodDelete
				path = "/admin/replies/" + args[0]
			}
			_, err := adminRequest(cmd, method, path, nil)
			return err
		},
	}
}

func adminRequest(cmd *cobra.Command, method, path string, body any) ([]byte, error) {
	cfg, err := loadCLIConfig()
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, strings.TrimRight(cfg.Server, "/")+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func loadCLIConfig() (*cliConfig, error) {
	cfg := &cliConfig{}
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".config", "listnr", "cli.toml")
		if _, err := toml.DecodeFile(path, cfg); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	if cliServer != "" {
		cfg.Server = cliServer
	}
	if cliToken != "" {
		cfg.Token = cliToken
	}
	if cfg.Server == "" || cfg.Token == "" {
		return nil, fmt.Errorf("server and token are required (use ~/.config/listnr/cli.toml or --server/--token)")
	}
	return cfg, nil
}

func runRepliesList(cmd *cobra.Command, _ []string) error {
	post, _ := cmd.Flags().GetString("post")
	hidden, _ := cmd.Flags().GetBool("hidden")
	q := url.Values{}
	if post != "" {
		q.Set("post", post)
	}
	if hidden {
		q.Set("hidden", "1")
	}
	path := "/admin/replies"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	b, err := adminRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var rows []struct {
		ID          int64  `json:"id"`
		APID        string `json:"ap_id"`
		ActorHandle string `json:"actor_handle"`
		ContentHTML string `json:"content_html"`
		Published   string `json:"published"`
		Hidden      bool   `json:"hidden"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHIDDEN\tAUTHOR\tPUBLISHED\tURL\tCONTENT")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%v\t%s\t%s\t%s\t%s\n", r.ID, r.Hidden, r.ActorHandle, r.Published, r.APID, stripTabs(r.ContentHTML))
	}
	return tw.Flush()
}

func runBlockList(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodGet, "/admin/blocks", nil)
	if err != nil {
		return err
	}
	var rows []struct {
		ID        int64  `json:"id"`
		Pattern   string `json:"pattern"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPATTERN\tCREATED")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\n", r.ID, r.Pattern, r.CreatedAt)
	}
	return tw.Flush()
}

func runBlockAdd(cmd *cobra.Command, args []string) error {
	_, err := adminRequest(cmd, http.MethodPost, "/admin/blocks", map[string]string{"pattern": args[0]})
	return err
}

func runBlockRemove(cmd *cobra.Command, args []string) error {
	_, err := adminRequest(cmd, http.MethodDelete, "/admin/blocks", map[string]string{"pattern": args[0]})
	return err
}

func runFollowersList(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodGet, "/admin/followers", nil)
	if err != nil {
		return err
	}
	var rows []struct {
		ID          int64  `json:"id"`
		ActorID     string `json:"actor_id"`
		Inbox       string `json:"inbox"`
		SharedInbox string `json:"shared_inbox"`
		FollowedAt  string `json:"followed_at"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tACTOR\tINBOX\tSHARED\tFOLLOWED")
	for _, r := range rows {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", r.ID, r.ActorID, r.Inbox, r.SharedInbox, r.FollowedAt)
	}
	return tw.Flush()
}

func runFollowerRemove(cmd *cobra.Command, args []string) error {
	if _, err := strconv.ParseInt(args[0], 10, 64); err != nil {
		return err
	}
	_, err := adminRequest(cmd, http.MethodDelete, "/admin/followers/"+args[0], nil)
	return err
}

func runPostsList(cmd *cobra.Command, _ []string) error {
	limit, _ := cmd.Flags().GetInt("limit")
	offset, _ := cmd.Flags().GetInt("offset")
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/admin/posts"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	b, err := adminRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var rows []struct {
		ID          int64  `json:"id"`
		URL         string `json:"url"`
		Title       string `json:"title"`
		APID        string `json:"ap_id"`
		PublishedAt string `json:"published_at"`
		DeletedAt   string `json:"deleted_at"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tPUBLISHED\tTITLE\tURL\tAP ID")
	for _, r := range rows {
		status := "live"
		if r.DeletedAt != "" {
			status = "deleted " + r.DeletedAt
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, status, r.PublishedAt, stripTabs(r.Title), r.URL, r.APID)
	}
	return tw.Flush()
}

func runPostDelete(cmd *cobra.Command, args []string) error {
	if _, err := strconv.ParseInt(args[0], 10, 64); err != nil {
		return fmt.Errorf("post id must be numeric (see `listnr posts list`): %w", err)
	}
	b, err := adminRequest(cmd, http.MethodDelete, "/admin/posts/"+args[0], nil)
	if err != nil {
		return err
	}
	var result struct {
		APID           string `json:"ap_id"`
		DeletedAt      string `json:"deleted_at"`
		AlreadyDeleted bool   `json:"already_deleted"`
		Queued         int    `json:"queued"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return err
	}
	if result.AlreadyDeleted {
		fmt.Fprintf(cmd.OutOrStdout(), "already deleted at %s: %s\n", result.DeletedAt, result.APID)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "deleted at %s: %s (%d deliveries queued)\n",
		result.DeletedAt, result.APID, result.Queued)
	return nil
}

func runActorPublish(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodPost, "/admin/actor/publish", nil)
	if err != nil {
		return err
	}
	var result struct {
		Published   bool   `json:"published"`
		Fingerprint string `json:"fingerprint"`
		Queued      int    `json:"queued"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return err
	}
	if !result.Published {
		fmt.Fprintf(cmd.OutOrStdout(), "profile unchanged (%s); nothing queued\n", result.Fingerprint)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "profile published (%s); %d deliveries queued\n",
		result.Fingerprint, result.Queued)
	return nil
}

func runStats(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodGet, "/admin/stats", nil)
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runPoll(cmd *cobra.Command, _ []string) error {
	_, err := adminRequest(cmd, http.MethodPost, "/admin/poll", nil)
	return err
}

func stripTabs(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\t", " "), "\n", " ")
}
