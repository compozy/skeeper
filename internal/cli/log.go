package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newLogCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.LogOptions
	cmd := &cobra.Command{
		Use:   "log <path>",
		Short: "Show sidecar history for a spec file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := service.Log(cmd.Context(), ".", args[0], opts)
			if err != nil {
				return err
			}
			if output == "" {
				_, err = fmt.Fprintln(stdout, "no sidecar history for", args[0])
				return err
			}
			_, err = fmt.Fprintln(stdout, output)
			return err
		},
	}
	cmd.Flags().BoolVar(&opts.Latest, "latest", false, "read the latest sidecar branch instead of the locked commit")
	cmd.Flags().StringVar(&opts.SourceBranch, "source-branch", "", "source branch to inspect")
	return cmd
}
