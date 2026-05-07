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
			if _, err := fmt.Fprintf(
				stdout,
				"sidecar:  %s\nbranch:   %s\n\n",
				status.Sidecar,
				status.Branch,
			); err != nil {
				return err
			}
			for _, namespace := range status.Namespaces {
				age := "never"
				if namespace.LastUnix > 0 {
					age = time.Since(time.Unix(namespace.LastUnix, 0)).Round(time.Second).String() + " ago"
				}
				if _, err := fmt.Fprintf(
					stdout,
					"namespace: %s\nsidecar branch: %s\nsynced:   %s  (%s)\nremote:   %s\ntracked files: %d\n\n",
					namespace.Name,
					namespace.Branch,
					emptyDefault(namespace.LastCommit, "none"),
					age,
					namespace.Remote,
					namespace.TrackedFiles,
				); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(stdout, "pending sync:  %d\n", status.PendingSync)
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
