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
		Short: "Pull remote specs, push local specs, and stage skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.SyncWorkflow(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if !result.OK {
				if _, err := fmt.Fprintln(stdout, "skeeper: sync blocked"); err != nil {
					return err
				}
				printDiffSummary(stdout, result.Pull.Hydrate.Plan)
				return fmt.Errorf("skeeper sync blocked")
			}
			if len(result.Pull.Hydrate.Restored) > 0 {
				if _, err := fmt.Fprintf(
					stdout,
					"skeeper: pulled %d spec(s)\n",
					len(result.Pull.Hydrate.Restored),
				); err != nil {
					return err
				}
			}
			return printSyncResult(stdout, result.Push, false)
		},
	}
	addSyncFlags(cmd, &opts)
	return cmd
}

func newPushCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.SyncOptions
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local specs into the sidecar repository and stage skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Push(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			return printSyncResult(stdout, result, opts.JSON)
		},
	}
	addSyncFlags(cmd, &opts)
	return cmd
}

func printSyncResult(stdout io.Writer, result sidecar.SyncResult, jsonOut bool) error {
	if jsonOut {
		return sidecar.PrintJSON(stdout, result)
	}
	if result.DryRun {
		_, err := fmt.Fprintf(stdout, "skeeper: dry run would push %d specs\n", result.ChangedFiles)
		return err
	}
	if result.ChangedFiles == 0 {
		_, err := fmt.Fprintln(stdout, "skeeper: no spec changes to push")
		return err
	}
	if _, err := fmt.Fprintf(
		stdout,
		"skeeper: pushed %d specs and staged skeeper.lock\n",
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
}

func addSyncFlags(cmd *cobra.Command, opts *sidecar.SyncOptions) {
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without mutating the sidecar or lockfile")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit staged skeeper changes in the main repository")
	cmd.Flags().StringVar(&opts.Message, "message", "", "main repository commit message used with --commit")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow plans that exceed configured guardrails")
	cmd.Flags().BoolVar(&opts.Mirror, "prune", false, "delete remote-only sidecar files that are absent locally")
}

func shortCLIHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
