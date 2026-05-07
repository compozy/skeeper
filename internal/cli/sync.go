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
			committedFiles := 0
			for _, namespace := range result.Namespaces {
				if namespace.Committed {
					committedFiles += namespace.ChangedFiles
				}
			}
			if _, err := fmt.Fprintf(stdout, "skeeper: synced %d specs to sidecar\n", committedFiles); err != nil {
				return err
			}
			for _, namespace := range result.Namespaces {
				if !namespace.Committed {
					continue
				}
				if _, err := fmt.Fprintf(
					stdout,
					"  %s -> %s: %d specs (%s)\n",
					namespace.Name,
					namespace.Branch,
					namespace.ChangedFiles,
					namespace.Commit,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.Pull, "pull", false, "pull/rebase the sidecar branch before syncing")
	cmd.Flags().BoolVar(&opts.Hook, "hook", false, "run in post-commit hook mode; always exits successfully")
	return cmd
}
