package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/reconcile"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newDiffCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.DiffOptions
	var classNames []string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show managed path drift against the locked sidecar state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			classes, err := parsePathClasses(classNames)
			if err != nil {
				return err
			}
			opts.Classes = classes
			summary, err := service.Diff(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, summary)
			}
			printDiffSummary(stdout, summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "show one namespace")
	cmd.Flags().StringArrayVar(&classNames, "class", nil, "path class to show; repeat for multiple classes")
	return cmd
}

func newReconcileCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.ReconcileOptions
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Resolve local and sidecar drift explicitly",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Reconcile(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			mutating := opts.AdoptLocal || opts.PruneLocal || opts.Merge || opts.Ours || opts.Theirs
			if opts.JSON {
				if err := sidecar.PrintJSON(stdout, result); err != nil {
					return err
				}
				if mutating && !result.OK {
					return fmt.Errorf("skeeper reconcile blocked")
				}
				return nil
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: reconcile ok")
				return err
			}
			if _, err := fmt.Fprintln(stdout, "skeeper: reconcile blocked"); err != nil {
				return err
			}
			printDiffSummary(stdout, result.Plan)
			if mutating {
				return fmt.Errorf("skeeper reconcile blocked")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the reconciliation without writing files")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(
		&opts.AdoptLocal,
		"adopt-local",
		false,
		"publish local-only or local-modified files to the sidecar",
	)
	cmd.Flags().BoolVar(&opts.PruneLocal, "prune-local", false, "move local-only files to rescue storage")
	cmd.Flags().BoolVar(&opts.Merge, "merge", false, "materialize conflict markers for both-modified files")
	cmd.Flags().BoolVar(&opts.Ours, "ours", false, "resolve conflicts in favor of local files")
	cmd.Flags().BoolVar(&opts.Theirs, "theirs", false, "resolve conflicts in favor of sidecar files")
	return cmd
}

func printDiffSummary(stdout io.Writer, summary reconcile.DiffSummary) {
	for _, namespace := range summary.Namespaces {
		_, _ = fmt.Fprintf(
			stdout,
			"namespace %s: extra=%d missing=%d modified=%d "+
				"local_deleted=%d remote_deleted=%d sidecar_modified=%d conflicts=%d "+
				"local_delete_conflicts=%d remote_delete_conflicts=%d "+
				"config_unowned=%d namespace_removed=%d\n",
			namespace.Name,
			namespace.Counts.LocalOnly,
			namespace.Counts.MissingLocal,
			namespace.Counts.LocalModified,
			namespace.Counts.LocalDeleted,
			namespace.Counts.RemoteDeleted,
			namespace.Counts.SidecarModified,
			namespace.Counts.BothModifiedConflict,
			namespace.Counts.LocalDeleteConflict,
			namespace.Counts.RemoteDeleteConflict,
			namespace.Counts.ConfigUnowned,
			namespace.Counts.NamespaceRemoved,
		)
	}
}

func parsePathClasses(values []string) ([]reconcile.PathClass, error) {
	classes := make([]reconcile.PathClass, 0, len(values))
	for _, value := range values {
		class := reconcile.PathClass(value)
		if !validPathClass(class) {
			return nil, fmt.Errorf("unknown path class %q", value)
		}
		classes = append(classes, class)
	}
	return classes, nil
}

func validPathClass(class reconcile.PathClass) bool {
	switch class {
	case reconcile.PathUnchanged,
		reconcile.PathMissingLocal,
		reconcile.PathLocalOnly,
		reconcile.PathLocalDeleted,
		reconcile.PathRemoteDeleted,
		reconcile.PathLocalModified,
		reconcile.PathSidecarModified,
		reconcile.PathBothModifiedConflict,
		reconcile.PathLocalDeleteConflict,
		reconcile.PathRemoteDeleteConflict,
		reconcile.PathNamespaceRemoved,
		reconcile.PathConfigUnowned:
		return true
	default:
		return false
	}
}
