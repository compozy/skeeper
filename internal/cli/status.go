package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newStatusCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.StatusOptions
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show sync health and the next Skeeper action",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStatusCmd(cmd, stdout, service, opts, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "write machine-readable JSON output")
	cmd.Flags().BoolVar(&opts.Check, "check", false, "exit non-zero when Skeeper health requires action")
	cmd.Flags().BoolVar(&opts.Paths, "paths", false, "show path-level drift details")
	return cmd
}

func runStatusCmd(
	cmd *cobra.Command,
	stdout io.Writer,
	service *sidecar.Service,
	opts sidecar.StatusOptions,
	jsonOut bool,
) error {
	current, err := service.Status(cmd.Context(), ".", opts)
	if err != nil {
		return err
	}
	if jsonOut {
		if err := sidecar.PrintJSON(stdout, current); err != nil {
			return err
		}
		return statusCheckError(opts.Check, current.OK)
	}
	if err := printStatus(stdout, current, opts); err != nil {
		return err
	}
	return statusCheckError(opts.Check, current.OK)
}

func printStatus(stdout io.Writer, status sidecar.Status, opts sidecar.StatusOptions) error {
	if err := printStatusHeader(stdout, status); err != nil {
		return err
	}
	for _, namespace := range status.Namespaces {
		if err := printNamespaceStatus(stdout, namespace, opts.Paths); err != nil {
			return err
		}
	}
	if err := printStatusState(stdout, status); err != nil {
		return err
	}
	for _, diag := range status.Diagnostics {
		if _, err := fmt.Fprintf(stdout, "warning: %s (%s)\n", diag.Message, diag.Code); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(stdout, "next:     %s\n", status.NextAction)
	return err
}

func printStatusHeader(stdout io.Writer, status sidecar.Status) error {
	_, err := fmt.Fprintf(
		stdout,
		"sidecar:  %s\nbranch:   %s\nlock:     %s\nhooks:    %s\n\n",
		status.Sidecar,
		status.Branch,
		lockStatus(status.LockPresent, status.LockCommit),
		okStatus(status.HooksOK),
	)
	return err
}

func printNamespaceStatus(stdout io.Writer, namespace sidecar.NamespaceStatus, paths bool) error {
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
	if !paths {
		return nil
	}
	return printNamespacePaths(stdout, namespace)
}

func printNamespacePaths(stdout io.Writer, namespace sidecar.NamespaceStatus) error {
	for _, path := range namespace.Paths {
		if path.Class == "unchanged" {
			continue
		}
		if _, err := fmt.Fprintf(stdout, "  %s\t%s\n", path.Class, path.Path); err != nil {
			return err
		}
	}
	if len(namespace.Paths) == 0 {
		return nil
	}
	_, err := fmt.Fprintln(stdout)
	return err
}

func printStatusState(stdout io.Writer, status sidecar.Status) error {
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
	if status.Bypass == nil {
		return nil
	}
	_, err := fmt.Fprintf(
		stdout,
		"bypass:  %s at %s\n",
		status.Bypass.Reason,
		status.Bypass.Time.Format(time.RFC3339),
	)
	return err
}

func statusCheckError(check bool, ok bool) error {
	if check && !ok {
		return fmt.Errorf("skeeper status check failed")
	}
	return nil
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

func okStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "needs repair"
}
