package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newStatusCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sidecar lock and sync status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := service.Status(cmd.Context(), ".")
			if err != nil {
				return err
			}
			if jsonOut {
				return sidecar.PrintJSON(stdout, status)
			}
			if _, err := fmt.Fprintf(
				stdout,
				"sidecar:  %s\nbranch:   %s\nlock:     %s\n\n",
				status.Sidecar,
				status.Branch,
				lockStatus(status.LockPresent, status.LockCommit),
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
					"namespace: %s\nsidecar branch: %s\nlocked:   %s\nsynced:   %s  (%s)\nremote:   %s\ntracked files: %d\n\n",
					namespace.Name,
					namespace.Branch,
					emptyDefault(namespace.LockedCommit, "none"),
					emptyDefault(namespace.LastCommit, "none"),
					age,
					namespace.Remote,
					namespace.TrackedFiles,
				); err != nil {
					return err
				}
			}
			if status.Transaction != nil {
				if _, err := fmt.Fprintf(
					stdout,
					"repair: active transaction %s (%s)\n",
					status.Transaction.ID,
					status.Transaction.Phase,
				); err != nil {
					return err
				}
			}
			if status.Bypass != nil {
				if _, err := fmt.Fprintf(
					stdout,
					"bypass:  %s at %s\n",
					status.Bypass.Reason,
					status.Bypass.Time.Format(time.RFC3339),
				); err != nil {
					return err
				}
			}
			for _, diag := range status.Diagnostics {
				if _, err := fmt.Fprintf(stdout, "warning: %s (%s)\n", diag.Message, diag.Code); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write machine-readable JSON output")
	return cmd
}

func lockStatus(present bool, commit string) string {
	if !present {
		return "missing"
	}
	if commit == "" {
		return "present"
	}
	return commit
}

func emptyDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
