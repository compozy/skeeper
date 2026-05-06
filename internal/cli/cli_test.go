package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/cli"
)

func TestExecute_VersionCommand(t *testing.T) {
	t.Run("Should print version metadata to stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(context.Background(), []string{"version"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "commit=") {
			t.Fatalf("expected commit metadata in output, got %q", stdout.String())
		}
	})
}

func TestExecute_UnknownCommand(t *testing.T) {
	t.Run("Should return non-zero exit code", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(context.Background(), []string{"definitely-not-a-cmd"}, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected non-zero exit code for unknown command")
		}
	})
}
