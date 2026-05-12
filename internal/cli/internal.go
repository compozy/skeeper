package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newInternalCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "internal",
		Short:  "Internal Skeeper plumbing",
		Hidden: true,
	}
	cmd.AddCommand(newInternalPreCommitCmd(stdout, service))
	cmd.AddCommand(newInternalPrePushCmd(stdout, service))
	cmd.AddCommand(newInternalMergeDriverCmd(stdout, service))
	cmd.AddCommand(newInternalRecordBypassCmd(service))
	return cmd
}

func newInternalPreCommitCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "pre-commit",
		Short: "Run managed pre-commit sync plumbing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Push(cmd.Context(), ".", sidecar.SyncOptions{Hook: true, Force: true})
			if err != nil {
				return err
			}
			return printSyncResult(stdout, result, false)
		},
	}
}

func newInternalPrePushCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "pre-push",
		Short: "Run managed pre-push verification plumbing",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Verify(cmd.Context(), ".", sidecar.VerifyOptions{Hook: true})
			if err != nil {
				return err
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
			return fmt.Errorf("skeeper pre-push failed")
		},
	}
}

func newInternalMergeDriverCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
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

func newInternalRecordBypassCmd(service *sidecar.Service) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "record-bypass",
		Short: "Record a strict hook bypass",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return service.RecordBypass(cmd.Context(), ".", reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "pre-commit bypass", "bypass reason")
	return cmd
}
