package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newLogCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "log <path>",
		Short: "Show sidecar history for a spec file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			output, err := service.Log(cmd.Context(), ".", args[0])
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
}
