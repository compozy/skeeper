package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newHydrateCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.HydrateOptions
	cmd := &cobra.Command{
		Use:   "hydrate",
		Short: "Restore spec files from the sidecar repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Hydrate(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return printJSONStatus(stdout, result, result.OK, "skeeper hydrate blocked")
			}
			if !result.OK {
				if _, err := fmt.Fprintln(stdout, "skeeper: hydrate blocked by local managed files"); err != nil {
					return err
				}
				printDiffSummary(stdout, result.Plan)
				return fmt.Errorf("skeeper hydrate blocked")
			}
			if result.DryRun {
				_, err = fmt.Fprintf(stdout, "skeeper: dry run would restore %d file(s)\n", len(result.Restored))
				return err
			}
			if _, err := fmt.Fprintf(stdout, "Restored %d files from sidecar", len(result.Restored)); err != nil {
				return err
			}
			if result.Commit != "" {
				if _, err := fmt.Fprintf(stdout, " at commit %s", result.Commit); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(stdout, ".")
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without writing files")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.KeepLocal, "keep-local", false, "restore safe files and keep local drift")
	cmd.Flags().
		BoolVar(&opts.AdoptLocal, "adopt-local", false, "publish local drift into the sidecar after safe restore")
	cmd.Flags().BoolVar(&opts.PruneLocal, "prune-local", false, "move local-only files to rescue before restore")
	cmd.Flags().BoolVar(&opts.Merge, "merge", false, "three-way merge conflicts using the hydration journal")
	cmd.Flags().BoolVar(&opts.Ours, "ours", false, "resolve conflicts using local worktree content")
	cmd.Flags().BoolVar(&opts.Theirs, "theirs", false, "resolve conflicts using locked sidecar content after rescue")
	return cmd
}
