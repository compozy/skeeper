package cli_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/cli"
	"github.com/compozy/skeeper/internal/config"
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

func TestExecute_HelpListsSidecarCommands(t *testing.T) {
	t.Run("Should expose v1 sidecar command surface", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cli.Execute(context.Background(), []string{"--help"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
		}
		help := stdout.String()
		for _, command := range []string{"init", "hydrate", "sync", "status", "log", "version"} {
			if !strings.Contains(help, command) {
				t.Fatalf("expected help to include %q, got:\n%s", command, help)
			}
		}
		if strings.Contains(help, "run") {
			t.Fatalf("did not expect scaffold run command in help:\n%s", help)
		}
	})
}

func TestExecute_InitNoDirectoryWarnsAndOmitsDirectory(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "shared-specs.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-b", "main")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	code := cli.Execute(
		context.Background(),
		[]string{
			"init",
			"--sidecar", remote,
			"--no-directory",
			"--patterns", "**/SPEC.md",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no directory namespace configured") {
		t.Fatalf("expected no-directory warning, got %q", stderr.String())
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Directory != "" {
		t.Fatalf("expected omitted directory, got %q", cfg.Directory)
	}
}

func TestExecute_InitRejectsMutuallyExclusiveSidecarFlags(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "shared-specs.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-b", "main")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	code := cli.Execute(
		context.Background(),
		[]string{
			"init",
			"--sidecar", remote,
			"--sidecar-name", "project-specs",
			"--patterns", "**/SPEC.md",
		},
		&stdout,
		&stderr,
	)
	if code == 0 {
		t.Fatalf("expected non-zero exit code, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(err) {
		t.Fatalf("expected config not to be written, stat err=%v", err)
	}
}

func TestExecute_InitRejectsMutuallyExclusiveDirectoryFlags(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "shared-specs.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-b", "main")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	code := cli.Execute(
		context.Background(),
		[]string{
			"init",
			"--sidecar", remote,
			"--directory", "project",
			"--no-directory",
			"--patterns", "**/SPEC.md",
		},
		&stdout,
		&stderr,
	)
	if code == 0 {
		t.Fatalf("expected non-zero exit code, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(err) {
		t.Fatalf("expected config not to be written, stat err=%v", err)
	}
}

func TestExecute_InitRejectsEmptyDirectoryFlag(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "shared-specs.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-b", "main")
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	code := cli.Execute(
		context.Background(),
		[]string{
			"init",
			"--sidecar", remote,
			"--directory", "",
			"--patterns", "**/SPEC.md",
		},
		&stdout,
		&stderr,
	)
	if code == 0 {
		t.Fatalf("expected non-zero exit code, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, config.Filename)); !os.IsNotExist(err) {
		t.Fatalf("expected config not to be written, stat err=%v", err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
