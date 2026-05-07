package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newAdoptCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.MutateOptions
	cmd := &cobra.Command{
		Use:   "adopt <path-or-glob>...",
		Short: "Move existing specs under sidecar coverage",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.Adopt(cmd.Context(), ".", args, opts)
			if err != nil {
				return err
			}
			return printMutateResult(stdout, "adopted", opts.JSON, result)
		},
	}
	addMutateFlags(cmd, &opts)
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

func newPatternCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	cmd := &cobra.Command{Use: "pattern", Short: "Inspect and update namespace patterns"}
	cmd.AddCommand(newPatternTestCmd(stdout, service))
	cmd.AddCommand(newPatternAddCmd(stdout, service))
	return cmd
}

func newPatternTestCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.PatternTestOptions
	cmd := &cobra.Command{
		Use:   "test <glob>",
		Short: "Show files a glob would match",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.PatternTest(cmd.Context(), ".", args[0], opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if _, err := fmt.Fprintf(stdout, "namespace: %s\nglob: %s\n", result.Namespace, result.Glob); err != nil {
				return err
			}
			for _, match := range result.Matches {
				if _, err := fmt.Fprintln(stdout, match); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "namespace to test against")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	return cmd
}

func newPatternAddCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.PatternAddOptions
	cmd := &cobra.Command{
		Use:   "add <glob>",
		Short: "Add a glob to a namespace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := service.PatternAdd(cmd.Context(), ".", args[0], opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.DryRun {
				_, err = fmt.Fprintf(stdout, "skeeper: dry run would update %s\n", result.ConfigPath)
				return err
			}
			_, err = fmt.Fprintf(stdout, "skeeper: updated %s and %s\n", result.ConfigPath, result.Gitignore)
			return err
		},
	}
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "namespace to update")
	cmd.Flags().StringArrayVar(&opts.Exclude, "exclude", nil, "exclude glob to add with the pattern")
	cmd.Flags().BoolVar(&opts.AdoptExisting, "adopt-existing", false, "adopt existing files matched by the new pattern")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the plan without writing changes")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "allow plans that exceed configured guardrails")
	cmd.Flags().BoolVar(&opts.Commit, "commit", false, "commit staged changes in the main repository")
	cmd.Flags().StringVar(&opts.Message, "message", "", "main repository commit message used with --commit")
	return cmd
}

func newFSCKCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.FSCKOptions
	cmd := &cobra.Command{
		Use:   "fsck",
		Short: "Compare working-tree specs against skeeper.lock",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.FSCK(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: working tree matches skeeper.lock")
				return err
			}
			for _, diag := range result.Diagnostics {
				if _, err := fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message); err != nil {
					return err
				}
			}
			return fmt.Errorf("skeeper fsck failed")
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().StringVar(&opts.SourceBranch, "source-branch", "", "expected source branch")
	return cmd
}

func newVerifyCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.VerifyOptions
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Validate skeeper.lock against the sidecar remote",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Verify(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if opts.JSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: lock verified")
				return err
			}
			for _, diag := range result.Diagnostics {
				if _, err := fmt.Fprintf(stdout, "%s: %s\n", diag.Code, diag.Message); err != nil {
					return err
				}
			}
			return fmt.Errorf("skeeper verify failed")
		},
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "write machine-readable JSON output")
	cmd.Flags().StringVar(&opts.SourceBranch, "source-branch", "", "expected source branch")
	cmd.Flags().BoolVar(&opts.Hook, "hook", false, "run in managed pre-push hook mode")
	markFlagHidden(cmd, "hook")
	return cmd
}

func newHooksCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	cmd := &cobra.Command{Use: "hooks", Short: "Install and check managed Git hooks"}
	var installJSON bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install managed pre-commit and pre-push hooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.HooksInstall(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if installJSON {
				return sidecar.PrintJSON(stdout, result)
			}
			_, err = fmt.Fprintf(stdout, "skeeper: installed hooks at %s and %s\n", result.PreCommit, result.PrePush)
			return err
		},
	}
	install.Flags().BoolVar(&installJSON, "json", false, "write machine-readable JSON output")
	var checkJSON bool
	check := &cobra.Command{
		Use:   "check",
		Short: "Check managed hook health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.HooksCheck(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if checkJSON {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.OK {
				_, err = fmt.Fprintln(stdout, "skeeper: hooks ok")
				return err
			}
			for _, diag := range result.Diagnostics {
				if _, err := fmt.Fprintln(stdout, diag); err != nil {
					return err
				}
			}
			return fmt.Errorf("skeeper hooks check failed")
		},
	}
	check.Flags().BoolVar(&checkJSON, "json", false, "write machine-readable JSON output")
	cmd.AddCommand(install, check)
	return cmd
}

func newMergeDriverCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "merge-driver [base current other]",
		Short: "Regenerate skeeper.lock during Git merges",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 || len(args) == 3 {
				return nil
			}
			return fmt.Errorf("merge-driver expects either no args or Git %%O %%A %%B paths")
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts sidecar.MergeDriverOptions
			if len(args) == 3 {
				opts.BasePath = args[0]
				opts.CurrentPath = args[1]
				opts.OtherPath = args[2]
			}
			result, err := service.MergeDriver(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if jsonOut {
				return sidecar.PrintJSON(stdout, result)
			}
			_, err = fmt.Fprintln(stdout, "skeeper: regenerated skeeper.lock")
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write machine-readable JSON output")
	return cmd
}

func newRepairCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	cmd := &cobra.Command{Use: "repair", Short: "Inspect and repair local skeeper state"}
	var jsonOut bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Show local repair state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.RepairStatus(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if jsonOut {
				return sidecar.PrintJSON(stdout, result)
			}
			if result.Transaction == nil && result.Bypass == nil {
				_, err = fmt.Fprintln(stdout, "skeeper: no repair state")
				return err
			}
			if result.Transaction != nil {
				if _, err := fmt.Fprintf(
					stdout,
					"transaction: %s (%s)\n",
					result.Transaction.ID,
					result.Transaction.Phase,
				); err != nil {
					return err
				}
			}
			if result.Bypass != nil {
				_, err = fmt.Fprintf(stdout, "bypass: %s\n", result.Bypass.Reason)
				return err
			}
			return nil
		},
	}
	status.Flags().BoolVar(&jsonOut, "json", false, "write machine-readable JSON output")
	resume := &cobra.Command{
		Use:   "resume",
		Short: "Resume by running a fresh sync",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.RepairResume(cmd.Context(), ".")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "skeeper: repair sync wrote %d namespace(s)\n", len(result.Namespaces))
			return err
		},
	}
	abort := &cobra.Command{
		Use:   "abort",
		Short: "Abort a repairable transaction before main-index mutation",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.RepairAbort(cmd.Context(), "."); err != nil {
				return err
			}
			_, err := fmt.Fprintln(stdout, "skeeper: repair state aborted")
			return err
		},
	}
	var reason string
	recordBypass := &cobra.Command{
		Use:    "record-bypass",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return service.RecordBypass(cmd.Context(), ".", reason)
		},
	}
	recordBypass.Flags().StringVar(&reason, "reason", "pre-commit bypass", "bypass reason")
	cmd.AddCommand(status, resume, abort, recordBypass)
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
