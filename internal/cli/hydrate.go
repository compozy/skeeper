package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newHydrateCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "hydrate",
		Short: "Restore spec files from the sidecar repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := service.Hydrate(cmd.Context(), ".")
			if err != nil {
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
}
