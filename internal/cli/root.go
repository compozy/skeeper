// Package cli exposes the skeeper command-line surface built on cobra.
package cli

import (
	"context"
	"errors"
	"io"

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

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "skeeper",
		Short:         "skeeper command-line interface",
		Long:          "skeeper is a Go CLI built on cobra.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.AddCommand(newRunCmd(stdout, stderr))
	cmd.AddCommand(newVersionCmd(stdout))
	return cmd
}
