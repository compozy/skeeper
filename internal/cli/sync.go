package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newSyncCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.SyncOptions
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Mirror spec files into the sidecar repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Sync(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if result.QueueFailed {
				_, err = fmt.Fprintf(
					stdout,
					"skeeper: sync failed and could not be queued; run 'skeeper sync' manually (%s)\n",
					result.QueueError,
				)
				return err
			}
			if result.Queued {
				_, err = fmt.Fprintln(stdout, "skeeper: sync queued, run 'skeeper sync' to retry")
				return err
			}
			if !result.Committed {
				if opts.Hook {
					return nil
				}
				_, err = fmt.Fprintln(stdout, "skeeper: no spec changes to sync")
				return err
			}
			if result.Commit == "" {
				_, err = fmt.Fprintf(stdout, "skeeper: synced %d specs to sidecar\n", result.ChangedFiles)
				return err
			}
			_, err = fmt.Fprintf(
				stdout,
				"skeeper: synced %d specs to sidecar (%s)\n",
				result.ChangedFiles,
				result.Commit,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.Pull, "pull", false, "pull/rebase the sidecar branch before syncing")
	cmd.Flags().BoolVar(&opts.Hook, "hook", false, "run in post-commit hook mode; always exits successfully")
	return cmd
}
