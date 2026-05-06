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

const noDirectoryWarning = "skeeper: warning: no directory namespace configured; " +
	"shared sidecars can collide at root and push to the same branch\n"

func newInitCmd(stdout, stderr io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.InitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create and connect a sidecar specs repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			usedTUI := false
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
				usedTUI = true
			} else {
				opts.DirectorySet = cmd.Flags().Changed("directory")
			}
			result, err := service.Init(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			if !usedTUI && opts.NoDirectory {
				if _, err := fmt.Fprint(stderr, noDirectoryWarning); err != nil {
					return err
				}
			}
			directory := ""
			if result.Config.Directory != "" {
				directory = fmt.Sprintf(" Directory %s.", result.Config.Directory)
			}
			_, err = fmt.Fprintf(
				stdout,
				"Done. Sidecar %s cloned into %s.%s Commit your code as usual - specs will sync automatically.\n",
				result.Config.Sidecar,
				sidecar.DirName,
				directory,
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
	cmd.Flags().StringVar(&opts.Directory, "directory", "", "sidecar directory namespace for this project")
	cmd.Flags().BoolVar(&opts.NoDirectory, "no-directory", false, "omit the sidecar directory namespace")
	cmd.Flags().StringVar(&opts.Bootstrap, "bootstrap", "", "optional install command stored in .skeeper.yml")
	cmd.Flags().StringArrayVar(&opts.Patterns, "patterns", nil, "spec glob pattern; repeat for multiple patterns")
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
		"directory",
		"no-directory",
		"bootstrap",
		"patterns",
	}, cmd.Flags().Changed)
}
