package cli

import (
	"fmt"
	"io"

	"github.com/compozy/skeeper/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the build version, commit, and build date",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(stdout, version.String())
			return err
		},
	}
}
