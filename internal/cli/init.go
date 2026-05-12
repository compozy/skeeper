package cli

import (
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/compozy/skeeper/internal/cli/inittui"
	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newInitCmd(stdout, _ io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.InitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create and connect a sidecar specs repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if shouldRunInitTUI(cmd) {
				defaults, err := service.InitDefaults(cmd.Context(), ".")
				if err != nil {
					return err
				}
				tuiOpts, err := inittui.Run(cmd.Context(), cmd.InOrStdin(), stdout, defaults)
				if err != nil {
					return err
				}
				opts = tuiOpts
			} else {
				opts.NamespaceSet = cmd.Flags().Changed("namespace")
			}
			result, err := service.Init(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			namespace := ""
			if len(result.Config.Namespaces) > 0 {
				namespace = fmt.Sprintf(" Namespace %s.", result.Config.Namespaces[0].Name)
			}
			_, err = fmt.Fprintf(
				stdout,
				"Done. Sidecar %s cloned into %s.%s Commit your code as usual - specs will sync automatically.\n",
				result.Config.Sidecar,
				sidecar.DirName,
				namespace,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&opts.Sidecar, "sidecar", "", "existing sidecar repository URL")
	cmd.Flags().StringVar(&opts.SidecarName, "sidecar-name", "", "GitHub sidecar repository name or OWNER/REPO")
	cmd.Flags().StringVar(
		&opts.Visibility,
		"visibility",
		"private",
		"GitHub repository visibility: private, public, or internal",
	)
	cmd.Flags().StringVar(&opts.Namespace, "namespace", "", "sidecar namespace for this project")
	cmd.Flags().StringVar(&opts.Bootstrap, "bootstrap", "", "optional install command stored in .skeeper.yml")
	cmd.Flags().StringArrayVar(&opts.Patterns, "patterns", nil, "spec glob pattern; repeat for multiple patterns")
	cmd.Flags().StringArrayVar(&opts.Track, "track", nil, "spec glob to track during init; repeat for multiple globs")
	return cmd
}

func isTerminalInput(cmd *cobra.Command) bool {
	if cmd.InOrStdin() != os.Stdin {
		return false
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func shouldRunInitTUI(cmd *cobra.Command) bool {
	if !isTerminalInput(cmd) {
		return false
	}
	if _, err := os.Stat(config.Filename); err == nil {
		return false
	}
	return !slices.ContainsFunc([]string{
		"sidecar",
		"sidecar-name",
		"visibility",
		"namespace",
		"bootstrap",
		"patterns",
		"track",
	}, cmd.Flags().Changed)
}
