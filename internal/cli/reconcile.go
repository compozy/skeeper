package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/compozy/skeeper/internal/reconcile"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newReconcileCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.ReconcileOptions
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Inspect or resolve working-tree drift against skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Reconcile(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return printJSONStatus(stdout, result, result.OK, "skeeper reconcile failed")
			}
			mutating := opts.AdoptLocal || opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs
			if result.OK && !mutating {
				_, err = fmt.Fprintln(stdout, "skeeper: working tree matches skeeper.lock")
				return err
			}
			printDiffSummary(stdout, result.Plan)
			if result.DryRun || !mutating {
				return nil
			}
			if result.Hydrate != nil && result.Hydrate.Rescue != nil {
				if _, err := fmt.Fprintf(stdout, "rescue: %s\n", result.Hydrate.Rescue.ID); err != nil {
					return err
				}
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: reconciled working tree")
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without writing files")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.AdoptLocal, "adopt-local", false, "publish local drift into the sidecar")
	cmd.Flags().BoolVar(&opts.PruneLocal, "prune-local", false, "move local-only files to rescue")
	cmd.Flags().BoolVar(&opts.Merge, "merge", false, "three-way merge conflicts using the hydration journal")
	cmd.Flags().BoolVar(&opts.Ours, "ours", false, "resolve conflicts using local worktree content")
	cmd.Flags().BoolVar(&opts.Theirs, "theirs", false, "resolve conflicts using locked sidecar content after rescue")
	return cmd
}

func newDiffCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.DiffOptions
	var classFlags []string
	var extra bool
	var missing bool
	var modified bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "List managed paths that differ from skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			classes, err := diffClasses(classFlags, extra, missing, modified)
			if err != nil {
				return err
			}
			opts.Classes = classes
			result, err := service.Diff(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			printDiffSummary(stdout, result)
			for _, namespace := range result.Namespaces {
				for _, path := range namespace.Paths {
					if path.Class == reconcile.PathUnchanged {
						continue
					}
					if _, err := fmt.Fprintf(
						stdout,
						"%s\t%s\t%s\n",
						namespace.Name,
						path.Class,
						path.Path,
					); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "filter to one namespace")
	cmd.Flags().StringArrayVar(&classFlags, "class", nil, "filter by diff class")
	cmd.Flags().BoolVar(&extra, "extra", false, "show local-only files")
	cmd.Flags().BoolVar(&missing, "missing", false, "show missing local files")
	cmd.Flags().BoolVar(&modified, "modified", false, "show modified files")
	return cmd
}

func newRescueCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	cmd := &cobra.Command{Use: "rescue", Short: "List and restore files moved aside by Skeeper"}
	var listJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List rescue manifests",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.RescueList(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if listJSON {
				return sidecar.PrintJSON(stdout, result)
			}
			for _, rescue := range result.Rescues {
				if _, err := fmt.Fprintf(
					stdout,
					"%s\t%s\t%d file(s)\n",
					rescue.ID,
					rescue.Operation,
					len(rescue.Files),
				); err != nil {
					return err
				}
			}
			return nil
		},
	}
	list.Flags().BoolVar(&listJSON, "json", false, "write machine-readable JSON output")

	var restoreOpts sidecar.RescueRestoreOptions
	restore := &cobra.Command{
		Use:   "restore <id> [path...]",
		Short: "Restore files from a rescue manifest",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.RescueRestore(cmd.Context(), ".", args[0], args[1:], restoreOpts)
			if err != nil {
				return err
			}
			if restoreOpts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			_, err = fmt.Fprintf(stdout, "skeeper: restored %d file(s) from rescue %s\n", len(result.Files), result.ID)
			return err
		},
	}
	restore.Flags().BoolVar(&restoreOpts.JSON, "json", false, "write machine-readable JSON output")
	restore.Flags().BoolVar(&restoreOpts.Overwrite, "overwrite", false, "overwrite existing restore targets")
	cmd.AddCommand(list, restore)
	return cmd
}

func newUpdateCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.UpdateOptions
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Fast-forward, verify, hydrate, fsck, and check hooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Update(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return printJSONStatus(stdout, result, result.OK, "skeeper update blocked")
			}
			if !result.OK {
				if _, err := fmt.Fprintln(stdout, "skeeper: update blocked"); err != nil {
					return err
				}
				printUpdateBlocked(stdout, result)
				return fmt.Errorf("skeeper update blocked")
			}
			_, err = fmt.Fprintf(
				stdout,
				"skeeper: update ok (git_updated=%t, restored=%d, hooks_ok=%t)\n",
				result.GitUpdated,
				len(result.Hydrate.Restored),
				result.HooksOK,
			)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.NoGit, "no-git", false, "skip main repository fetch and fast-forward")
	cmd.Flags().
		StringVar(&opts.Reconcile, "reconcile", "report", "mode: report, keep-local, adopt-local, prune-local, merge")
	cmd.Flags().BoolVar(&opts.Ours, "ours", false, "resolve conflicts using local worktree content")
	cmd.Flags().BoolVar(&opts.Theirs, "theirs", false, "resolve conflicts using locked sidecar content after rescue")
	return cmd
}

func diffClasses(flags []string, extra, missing, modified bool) ([]reconcile.PathClass, error) {
	classes := make([]reconcile.PathClass, 0, len(flags)+4)
	for _, raw := range flags {
		class := reconcile.PathClass(strings.TrimSpace(raw))
		switch class {
		case reconcile.PathUnchanged,
			reconcile.PathMissingLocal,
			reconcile.PathLocalOnly,
			reconcile.PathLocalModified,
			reconcile.PathSidecarModified,
			reconcile.PathBothModifiedConflict,
			reconcile.PathNamespaceRemoved,
			reconcile.PathConfigUnowned:
			classes = append(classes, class)
		default:
			return nil, fmt.Errorf("unknown diff class %q", raw)
		}
	}
	if extra {
		classes = append(classes, reconcile.PathLocalOnly)
	}
	if missing {
		classes = append(classes, reconcile.PathMissingLocal)
	}
	if modified {
		classes = append(
			classes,
			reconcile.PathLocalModified,
			reconcile.PathSidecarModified,
			reconcile.PathBothModifiedConflict,
		)
	}
	return classes, nil
}

func printDiffSummary(stdout io.Writer, summary reconcile.DiffSummary) {
	for _, namespace := range summary.Namespaces {
		_, _ = fmt.Fprintf(
			stdout,
			"namespace %s: extra=%d missing=%d modified=%d "+
				"sidecar_modified=%d conflicts=%d config_unowned=%d namespace_removed=%d\n",
			namespace.Name,
			namespace.Counts.LocalOnly,
			namespace.Counts.MissingLocal,
			namespace.Counts.LocalModified,
			namespace.Counts.SidecarModified,
			namespace.Counts.BothModifiedConflict,
			namespace.Counts.ConfigUnowned,
			namespace.Counts.NamespaceRemoved,
		)
	}
}

func printUpdateBlocked(stdout io.Writer, result sidecar.UpdateResult) {
	if !result.Verify.OK && len(result.Verify.Diagnostics) > 0 {
		for _, diag := range result.Verify.Diagnostics {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message)
		}
		return
	}
	if len(result.Hydrate.Plan.Namespaces) > 0 {
		printDiffSummary(stdout, result.Hydrate.Plan)
		return
	}
	if len(result.FSCK.Diagnostics) > 0 {
		for _, diag := range result.FSCK.Diagnostics {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message)
		}
	}
}

func sidecarFSCKSummary(result sidecar.FSCKResult) reconcile.DiffSummary {
	return reconcile.SummarizeDiff(result.Namespaces)
}
