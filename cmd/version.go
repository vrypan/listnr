package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/vrypan/listnr/internal/buildinfo"
)

func init() {
	var asJSON, remote bool
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print build version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Current()
			if remote {
				body, err := adminRequest(cmd, http.MethodGet, "/admin/stats", nil)
				if err != nil {
					return err
				}
				var response struct {
					Build buildinfo.Details `json:"build"`
				}
				if err := json.Unmarshal(body, &response); err != nil {
					return err
				}
				if response.Build.Version == "" {
					return fmt.Errorf("running server did not report build information")
				}
				info = response.Build
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return nil
		},
	}
	versionCmd.Flags().BoolVar(&asJSON, "json", false, "print build information as JSON")
	versionCmd.Flags().BoolVar(&remote, "remote", false, "print the running server's build information")
	rootCmd.AddCommand(versionCmd)
}
