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
		Short: "Mirror spec files into the sidecar repository and stage skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Sync(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.DryRun {
				_, err = fmt.Fprintf(stdout, "skeeper: dry run would sync %d specs\n", result.ChangedFiles)
				return err
			}
			if result.ChangedFiles == 0 {
				_, err = fmt.Fprintln(stdout, "skeeper: no spec changes to sync")
				return err
			}
			if _, err := fmt.Fprintf(
				stdout,
				"skeeper: synced %d specs and staged skeeper.lock\n",
				result.ChangedFiles,
			); err != nil {
				return err
			}
			for _, namespace := range result.Namespaces {
				if _, err := fmt.Fprintf(
					stdout,
					"  %s -> %s: %d specs (%s, %s)\n",
					namespace.Name,
					namespace.Branch,
					namespace.ChangedFiles,
					shortCLIHash(namespace.Commit),
					namespace.Digest,
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without mutating the sidecar or lockfile")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit staged skeeper changes in the main repository")
	cmd.Flags().StringVar(&opts.Message, "message", "", "main repository commit message used with --commit")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow plans that exceed configured guardrails")
	cmd.Flags().BoolVar(&opts.Hook, "hook", false, "run in managed pre-commit hook mode")
	markFlagHidden(cmd, "hook")
	return cmd
}

func markFlagHidden(cmd *cobra.Command, name string) {
	if err := cmd.Flags().MarkHidden(name); err != nil {
		cmd.RunE = func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("configure hidden flag %q: %w", name, err)
		}
	}
}

func shortCLIHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
