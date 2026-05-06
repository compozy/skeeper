package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/sidecar"
	"github.com/spf13/cobra"
)

func newInitCmd(stdout io.Writer, service *sidecar.Service) *cobra.Command {
	var opts sidecar.InitOptions
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create and connect a sidecar specs repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !cmd.Flags().Changed("sidecar-name") {
				value, err := promptString(cmd, stdout, "Sidecar repo name", opts.SidecarName)
				if err != nil {
					return err
				}
				opts.SidecarName = value
			}
			if !cmd.Flags().Changed("patterns") {
				values, err := promptPatterns(cmd, stdout)
				if err != nil {
					return err
				}
				opts.Patterns = values
			}
			result, err := service.Init(cmd.Context(), ".", opts)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(
				stdout,
				"Done. Sidecar %s cloned into %s. Commit your code as usual - specs will sync automatically.\n",
				result.Config.Sidecar,
				sidecar.DirName,
			)
			return err
		},
	}
	cmd.Flags().StringVar(&opts.SidecarName, "sidecar-name", "", "GitHub sidecar repository name or OWNER/REPO")
	cmd.Flags().StringVar(
		&opts.Visibility,
		"visibility",
		"private",
		"GitHub repository visibility: private, public, or internal",
	)
	cmd.Flags().StringVar(&opts.Bootstrap, "bootstrap", "", "optional install command stored in .skeeper.yml")
	cmd.Flags().StringArrayVar(&opts.Patterns, "patterns", nil, "spec glob pattern; repeat for multiple patterns")
	return cmd
}

func promptString(cmd *cobra.Command, stdout io.Writer, label, fallback string) (string, error) {
	if !isTerminalInput(cmd) {
		return fallback, nil
	}
	if fallback == "" {
		if _, err := fmt.Fprintf(stdout, "%s: ", label); err != nil {
			return "", err
		}
	} else if _, err := fmt.Fprintf(stdout, "%s [%s]: ", label, fallback); err != nil {
		return "", err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", fmt.Errorf("read prompt: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, nil
	}
	return line, nil
}

func promptPatterns(cmd *cobra.Command, stdout io.Writer) ([]string, error) {
	if !isTerminalInput(cmd) {
		return nil, nil
	}
	if _, err := fmt.Fprintln(stdout, "Patterns (one per line, empty to finish):"); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(cmd.InOrStdin())
	var patterns []string
	for {
		if _, err := fmt.Fprint(stdout, "> "); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			return nil, fmt.Errorf("read pattern: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		patterns = append(patterns, line)
	}
	if len(patterns) == 0 {
		return config.DefaultPatterns(), nil
	}
	return patterns, nil
}

func isTerminalInput(cmd *cobra.Command) bool {
	if cmd.InOrStdin() != os.Stdin {
		return true
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
