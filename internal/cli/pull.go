package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newPullCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.PullOptions
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Pull remote specs from the sidecar into the working tree",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Pull(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if !result.OK {
				if _, err := fmt.Fprintln(stdout, "skeeper: pull blocked"); err != nil {
					return err
				}
				printDiffSummary(stdout, result.Hydrate.Plan)
				return fmt.Errorf("skeeper pull blocked")
			}
			_, err = fmt.Fprintf(
				stdout,
				"skeeper: pull ok (git_updated=%t, restored=%d, lock_updated=%t)\n",
				result.GitUpdated,
				len(result.Hydrate.Restored),
				result.LockUpdated,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.NoGit, "no-git", false, "skip main repository fetch and fast-forward")
	return cmd
}

func newRestoreCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.RestoreOptions
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "restore [path...]",
		Short: "Restore local specs from the locked sidecar state",
		Args: func(_ *cobra.Command, args []string) error {
			if opts.All {
				if len(args) != 0 {
					return fmt.Errorf("restore --all cannot be combined with explicit paths")
				}
				return nil
			}
			if len(args) == 0 {
				return fmt.Errorf("restore requires at least one path or --all")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Paths = append([]string(nil), args...)
			result, err := service.Restore(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSONStatus(stdout, result, result.OK, "skeeper restore failed")
			}
			if !result.OK {
				if _, err := fmt.Fprintln(stdout, "skeeper: restore failed"); err != nil {
					return err
				}
				for _, diag := range result.Diagnostics {
					if _, err := fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message); err != nil {
						return err
					}
				}
				return fmt.Errorf("skeeper restore blocked")
			}
			if result.DryRun {
				_, err = fmt.Fprintf(stdout, "skeeper: dry run would restore %d spec(s)\n", len(result.Restored))
				return err
			}
			_, err = fmt.Fprintf(
				stdout,
				"skeeper: restored %d spec(s) from locked sidecar state\n",
				len(result.Restored),
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without writing files")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.All, "all", false, "restore every locked managed path")
	return cmd
}
