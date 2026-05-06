// Package cli exposes the skeeper command-line surface built on cobra.
package cli

import (
	"context"
	"errors"
	"io"

	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

// Execute parses args and dispatches to the matching subcommand. The returned
// integer is the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		return 1
	}
	return 0
}

func newRootCmd(stdout, _ io.Writer) *cobra.Command {
	service := sidecar.New(&gitexec.ExecRunner{})
	cmd := &cobra.Command{
		Use:           "skeeper",
		Short:         "Version spec artifacts in a sidecar Git repository",
		Long:          "skeeper mirrors Spec-Driven Development artifacts into a sidecar Git repository.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.AddCommand(newInitCmd(stdout, service))
	cmd.AddCommand(newHydrateCmd(stdout, service))
	cmd.AddCommand(newSyncCmd(stdout, service))
	cmd.AddCommand(newStatusCmd(stdout, service))
	cmd.AddCommand(newLogCmd(stdout, service))
	cmd.AddCommand(newVersionCmd(stdout))
	return cmd
}
