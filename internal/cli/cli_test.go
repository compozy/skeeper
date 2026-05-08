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
	"github.com/compozy/skeeper/internal/gitexec"
	"github.com/compozy/skeeper/internal/sidecar"
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
		for _, command := range []string{
			"init",
			"hydrate",
			"sync",
			"reconcile",
			"diff",
			"adopt",
			"untrack",
			"pattern",
			"fsck",
			"verify",
			"hooks",
			"merge-driver",
			"rescue",
			"update",
			"repair",
			"status",
			"log",
			"version",
		} {
			if !strings.Contains(help, command) {
				t.Fatalf("expected help to include %q, got:\n%s", command, help)
			}
		}
		if strings.Contains(help, "\n  run ") {
			t.Fatalf("did not expect scaffold run command in help:\n%s", help)
		}
	})
}

func TestExecute_SubcommandHelp(t *testing.T) {
	commands := [][]string{
		{"init"},
		{"hydrate"},
		{"sync"},
		{"reconcile"},
		{"diff"},
		{"adopt"},
		{"untrack"},
		{"pattern"},
		{"pattern", "test"},
		{"pattern", "add"},
		{"fsck"},
		{"verify"},
		{"hooks"},
		{"hooks", "install"},
		{"hooks", "check"},
		{"merge-driver"},
		{"rescue"},
		{"rescue", "list"},
		{"rescue", "restore"},
		{"update"},
		{"repair"},
		{"repair", "status"},
		{"repair", "resume"},
		{"repair", "abort"},
		{"status"},
		{"log"},
		{"version"},
	}

	for _, command := range commands {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, command...), "--help")
			code := cli.Execute(context.Background(), args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("expected help exit 0, got %d (stderr=%q)", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("expected usage output, got %q", stdout.String())
			}
		})
	}
}

func TestExecute_InitWritesNamespace(t *testing.T) {
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
			"--namespace", "skills",
			"--patterns", "**/SPEC.md",
		},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Namespaces) != 1 || cfg.Namespaces[0].Name != "skills" {
		t.Fatalf("expected skills namespace, got %#v", cfg.Namespaces)
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

func TestExecute_InitRejectsEmptyNamespaceFlag(t *testing.T) {
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
			"--namespace", "",
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

func TestExecute_JSONFailuresReturnNonZero(t *testing.T) {
	root := setupCLISkeeperProject(t)
	t.Chdir(root)
	writeCLITestFile(t, root, "src/auth/SPEC.md", "# Drift\n")

	for _, command := range [][]string{
		{"fsck", "--json"},
		{"hydrate", "--json"},
		{"update", "--no-git", "--json"},
	} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Execute(context.Background(), command, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected non-zero exit code, stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), `"ok": false`) {
				t.Fatalf("expected JSON failure body, got stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func setupCLISkeeperProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(t.TempDir(), "shared-specs.git")
	git(t, "", "init", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.email", "skeeper-test@example.com")
	git(t, root, "config", "user.name", "Skeeper Test")
	cfg := config.Config{
		Sidecar: remote,
		Namespaces: []config.Namespace{{
			Name:     "project",
			Patterns: []string{"**/SPEC.md"},
		}},
	}
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
	writeCLITestFile(t, root, "README.md", "# project\n")
	git(t, root, "add", "README.md", config.Filename)
	git(t, root, "commit", "-m", "bootstrap")
	writeCLITestFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	service := sidecar.New(&gitexec.ExecRunner{})
	if _, err := service.Sync(context.Background(), root, sidecar.SyncOptions{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	return root
}

func writeCLITestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
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
