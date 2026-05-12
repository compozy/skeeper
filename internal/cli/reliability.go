package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newTrackCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.TrackOptions
	cmd := &cobra.Command{
		Use:   "track <glob>",
		Short: "Track a spec glob with Skeeper",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.Track(cmd.Context(), ".", args[0], opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.DryRun {
				_, err = fmt.Fprintf(stdout, "skeeper: dry run would track %s\n", args[0])
				return err
			}
			if result.Synced {
				_, err = fmt.Fprintf(stdout, "skeeper: tracking %s and synced matching specs\n", args[0])
				return err
			}
			_, err = fmt.Fprintf(stdout, "skeeper: tracking %s\n", args[0])
			return err
		},
	}
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "namespace to update")
	cmd.Flags().StringArrayVar(&opts.Exclude, "exclude", nil, "exclude glob to add with the tracked glob")
	cmd.Flags().BoolVar(&opts.Sync, "sync", false, "publish matching existing specs after tracking")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without writing changes")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow plans that exceed configured guardrails")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit staged changes in the main repository")
	cmd.Flags().StringVar(&opts.Message, "message", "", "main repository commit message used with --commit")
	return cmd
}

func newUntrackCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.MutateOptions
	cmd := &cobra.Command{
		Use:   "untrack <path-or-glob>...",
		Short: "Stop tracking specs in the main repository after sidecar sync",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.Untrack(cmd.Context(), ".", args, opts)
			if err != nil {
				return err
			}
			return printMutateResult(stdout, "untracked", opts.JSON, result)
		},
	}
	addMutateFlags(cmd, &opts)
	return cmd
}

func newRepairCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.RepairOptions
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Diagnose and repair local Skeeper state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Repair(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				if err := sidecar.PrintJSON(stdout, result); err != nil {
					return err
				}
				if !result.OK {
					return fmt.Errorf("skeeper repair check failed")
				}
				return nil
			}
			for _, action := range result.Actions {
				if _, err := fmt.Fprintf(stdout, "fixed: %s\n", action.Message); err != nil {
					return err
				}
			}
			for _, rescue := range result.Rescues {
				if _, err := fmt.Fprintf(
					stdout,
					"rescue: %s (%d file(s))\n",
					rescue.ID,
					len(rescue.Files),
				); err != nil {
					return err
				}
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: repair ok")
				return err
			}
			if _, err := fmt.Fprintln(stdout, "skeeper: repair needed"); err != nil {
				return err
			}
			for _, diag := range result.Diagnostics {
				if _, err := fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message); err != nil {
					return err
				}
			}
			return fmt.Errorf("skeeper repair check failed")
		},
	}
	cmd.Flags().BoolVar(&opts.Check, "check", false, "diagnose without writing safe repairs")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	return cmd
}

func addMutateFlags(cmd *cobra.Command, opts *sidecar.MutateOptions) {
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without mutating files")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow plans that exceed configured guardrails")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit staged changes in the main repository")
	cmd.Flags().StringVar(&opts.Message, "message", "", "main repository commit message used with --commit")
}

func printMutateResult(stdout io.Writer, verb string, jsonOut bool, result sidecar.MutateResult) error {
	if jsonOut {
		return sidecar.PrintJSON(stdout, result)
	}
	if result.DryRun {
		_, err := fmt.Fprintf(stdout, "skeeper: dry run would update %d target(s)\n", len(result.Changed))
		return err
	}
	_, err := fmt.Fprintf(stdout, "skeeper: %s %d target(s)\n", verb, len(result.Changed))
	return err
}
