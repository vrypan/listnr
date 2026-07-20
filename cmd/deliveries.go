package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// maxDisplayedError bounds the last-error column so one long TLS message does
// not destroy the table. The full text stays in the JSON response.
const maxDisplayedError = 60

type deliveryRow struct {
	ID            int64  `json:"id"`
	InboxURL      string `json:"inbox_url"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at"`
	LastError     string `json:"last_error"`
	ActivityType  string `json:"activity_type"`
	ActivityID    string `json:"activity_id"`
}

func runDeliveriesList(cmd *cobra.Command, _ []string) error {
	status, _ := cmd.Flags().GetString("status")
	switch status {
	case "", "pending", "failed", "done":
	default:
		return fmt.Errorf("unknown status %q (want pending, failed, or done)", status)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	offset, _ := cmd.Flags().GetInt("offset")

	q := url.Values{}
	if status != "" {
		q.Set("status", status)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	path := "/admin/deliveries"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	b, err := adminRequest(cmd, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	var rows []deliveryRow
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTRIES\tNEXT\tINBOX\tACTIVITY\tERROR")
	for _, r := range rows {
		activity := r.ActivityType
		if r.ActivityID != "" {
			activity += " " + r.ActivityID
		}
		fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\t%s\t%s\n", r.ID, r.Status, r.Attempts,
			r.NextAttemptAt, r.InboxURL, stripTabs(activity), truncate(stripTabs(r.LastError)))
	}
	return tw.Flush()
}

func runDeliveryRetry(cmd *cobra.Command, args []string) error {
	if err := requireNumericID(args[0]); err != nil {
		return err
	}
	_, err := adminRequest(cmd, http.MethodPost, "/admin/deliveries/"+args[0]+"/retry", nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "delivery %s requeued\n", args[0])
	return nil
}

func runDeliveryDelete(cmd *cobra.Command, args []string) error {
	if err := requireNumericID(args[0]); err != nil {
		return err
	}
	_, err := adminRequest(cmd, http.MethodDelete, "/admin/deliveries/"+args[0], nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "delivery %s deleted\n", args[0])
	return nil
}

func runDeliveriesRetryFailed(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodPost, "/admin/deliveries/retry-failed", nil)
	if err != nil {
		return err
	}
	var result struct {
		Retried int `json:"retried"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%d failed deliveries requeued\n", result.Retried)
	return nil
}

func requireNumericID(raw string) error {
	if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
		return fmt.Errorf("delivery id must be numeric (see `listnr deliveries list`): %w", err)
	}
	return nil
}

func truncate(s string) string {
	if len(s) <= maxDisplayedError {
		return s
	}
	return s[:maxDisplayedError-1] + "…"
}
