package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

type moveStatus struct {
	Moved             bool   `json:"moved"`
	Target            string `json:"target"`
	ActivityID        string `json:"activity_id"`
	TargetFingerprint string `json:"target_fingerprint"`
	MovedAt           string `json:"moved_at"`
	AlreadyMoved      bool   `json:"already_moved"`
	Queued            int    `json:"queued"`
}

func runActorMoveStatus(cmd *cobra.Command, _ []string) error {
	b, err := adminRequest(cmd, http.MethodGet, "/admin/actor/move", nil)
	if err != nil {
		return err
	}
	var status moveStatus
	if err := json.Unmarshal(b, &status); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if !status.Moved {
		fmt.Fprintln(out, "not moved")
		return nil
	}
	fmt.Fprintf(out, "moved to %s at %s\n", status.Target, status.MovedAt)
	fmt.Fprintf(out, "  activity:           %s\n", status.ActivityID)
	fmt.Fprintf(out, "  target fingerprint: %s\n", status.TargetFingerprint)
	return nil
}

func runActorMove(cmd *cobra.Command, _ []string) error {
	target, _ := cmd.Flags().GetString("to")
	confirmed, _ := cmd.Flags().GetBool("yes")
	if target == "" {
		return fmt.Errorf("--to is required, e.g. --to https://mastodon.example/users/me")
	}
	// A bare handle would have to be resolved by guessing; this version
	// requires the operator to name the actor URL they have verified.
	if !strings.HasPrefix(target, "https://") {
		return fmt.Errorf("--to must be a full https actor URL, not a handle: %q", target)
	}
	if _, err := url.Parse(target); err != nil {
		return fmt.Errorf("--to is not a valid URL: %w", err)
	}
	if !confirmed {
		return fmt.Errorf(
			"moving this actor to %s is irreversible: followers are told to follow it instead, "+
				"and listnr cannot undo that. Re-run with --yes to confirm", target)
	}

	b, err := adminRequest(cmd, http.MethodPost, "/admin/actor/move", map[string]string{"target": target})
	if err != nil {
		return err
	}
	var result moveStatus
	if err := json.Unmarshal(b, &result); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if result.AlreadyMoved {
		fmt.Fprintf(out, "already moved to %s at %s; nothing queued\n", result.Target, result.MovedAt)
		return nil
	}
	fmt.Fprintf(out, "moved to %s at %s (%d deliveries queued)\n",
		result.Target, result.MovedAt, result.Queued)
	fmt.Fprintln(out, "keep this instance online and backed up: it is part of the migration.")
	return nil
}
