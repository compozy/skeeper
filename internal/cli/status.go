package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newStatusCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show sidecar sync status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := service.Status(cmd.Context(), ".")
			if err != nil {
				return err
			}
			age := "never"
			if status.LastUnix > 0 {
				age = time.Since(time.Unix(status.LastUnix, 0)).Round(time.Second).String() + " ago"
			}
			format := "sidecar:  %s\nbranch:   %s\nsynced:   %s  (%s)\nremote:   %s\n\n" +
				"tracked files: %d\npending sync:  %d\n"
			_, err = fmt.Fprintf(
				stdout,
				format,
				status.Sidecar,
				status.Branch,
				emptyDefault(status.LastCommit, "none"),
				age,
				status.Remote,
				status.TrackedFiles,
				status.PendingSync,
			)
			return err
		},
	}
}

func emptyDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
