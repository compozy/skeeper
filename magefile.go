//go:build mage

package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	golangciLintVersion   = "v2.11.4"
	goplsModernizeVersion = "v0.21.1"
	gotestsumVersion      = "v1.13.0"
	goreleaserVersion     = "v2.6.1"
	binDir                = "bin"
	appBinary             = "skeeper"
	versionPackage        = "github.com/compozy/skeeper/internal/version"
	coverageOut           = "coverage.out"
	coverageHTML          = "coverage.html"
)

var Default = Verify

// Deps tidies and verifies the module graph.
func Deps() error {
	return sh.RunV("go", "mod", "tidy")
}

// Fmt formats every Go file outside of vendor and dot directories.
func Fmt() error {
	files, err := goFiles(".")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"-w"}, files...)
	return sh.RunV("gofmt", args...)
}

// Lint runs golangci-lint v2 with auto-fix and then the gopls modernize analyzer.
func Lint() error {
	if err := sh.RunV(
		"go",
		"run",
		"github.com/golangci/golangci-lint/v2/cmd/golangci-lint@"+golangciLintVersion,
		"run",
		"--fix",
		"--allow-parallel-runners",
		"./...",
	); err != nil {
		return err
	}
	return Modernize()
}

// Modernize applies gopls' modernize idiom suggestions (min/max/slices/etc).
func Modernize() error {
	return sh.RunV(
		"go",
		"run",
		"golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@"+goplsModernizeVersion,
		"-fix",
		"./...",
	)
}

// Test runs unit tests via gotestsum with the race detector.
func Test() error {
	return runWithEnv(
		map[string]string{"CGO_ENABLED": "1"},
		"go", "run", "gotest.tools/gotestsum@"+gotestsumVersion,
		"--format", "pkgname", "--", "-race", "-parallel=4", "./...",
	)
}

// TestIntegration runs tests under the `integration` build tag.
func TestIntegration() error {
	return runWithEnv(
		map[string]string{"CGO_ENABLED": "1"},
		"go", "run", "gotest.tools/gotestsum@"+gotestsumVersion,
		"--format", "pkgname", "--", "-race", "-parallel=4", "-tags", "integration", "./...",
	)
}

// Cover produces a race-enabled coverage report (coverage.out + coverage.html).
func Cover() error {
	if err := runWithEnv(
		map[string]string{"CGO_ENABLED": "1"},
		"go", "test", "-race", "-covermode=atomic", "-coverprofile="+coverageOut, "./...",
	); err != nil {
		return err
	}
	return sh.RunV("go", "tool", "cover", "-html="+coverageOut, "-o", coverageHTML)
}

// Build compiles the application binary into bin/app with version ldflags.
func Build() error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(binDir, appBinary)
	return sh.RunV("go", "build", "-trimpath", "-ldflags", buildLDFlags(), "-o", out, "./cmd/skeeper")
}

// Verify runs the blocking gate: fmt -> lint -> test -> build.
func Verify() {
	mg.SerialDeps(Fmt, Lint, Test, Build)
}

// Tools pre-installs the CLI helpers used by the workflows.
func Tools() error {
	binaries := []string{
		"gotest.tools/gotestsum@" + gotestsumVersion,
		"github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + golangciLintVersion,
		"golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@" + goplsModernizeVersion,
		"github.com/goreleaser/goreleaser/v2@" + goreleaserVersion,
	}
	for _, pkg := range binaries {
		if err := sh.RunV("go", "install", pkg); err != nil {
			return err
		}
	}
	return nil
}

// HooksInstall wires husky git hooks; requires `bun install` to have run.
func HooksInstall() error {
	if _, err := exec.LookPath("bunx"); err != nil {
		return fmt.Errorf("bunx not found in PATH; run `bun install` first: %w", err)
	}
	return sh.RunV("bunx", "husky")
}

// ReleaseSnapshot produces a local goreleaser snapshot under dist/.
func ReleaseSnapshot() error {
	return sh.RunV(
		"go", "run", "github.com/goreleaser/goreleaser/v2@"+goreleaserVersion,
		"release", "--snapshot", "--clean",
	)
}

// BunLint runs the JS/TS toolchain (oxfmt + oxlint) on non-Go files.
func BunLint() error {
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun not found in PATH: %w", err)
	}
	return sh.RunV("bun", "run", "lint")
}

// BunFormat applies oxfmt across the repository.
func BunFormat() error {
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun not found in PATH: %w", err)
	}
	return sh.RunV("bun", "run", "format")
}

// BunFormatCheck verifies oxfmt formatting without writing changes.
func BunFormatCheck() error {
	if _, err := exec.LookPath("bun"); err != nil {
		return fmt.Errorf("bun not found in PATH: %w", err)
	}
	return sh.RunV("bun", "run", "format:check")
}

func goFiles(root string) ([]string, error) {
	var files []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

func buildLDFlags() string {
	version := gitOutput("describe", "--tags", "--always", "--dirty")
	if version == "" {
		version = "dev"
	}
	commit := gitOutput("rev-parse", "--short", "HEAD")
	if commit == "" {
		commit = "none"
	}
	buildDate := time.Now().UTC().Format(time.RFC3339)

	return strings.Join([]string{
		"-s -w",
		"-X " + versionPackage + ".Version=" + version,
		"-X " + versionPackage + ".Commit=" + commit,
		"-X " + versionPackage + ".BuildDate=" + buildDate,
	}, " ")
}

func gitOutput(args ...string) string {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runWithEnv(env map[string]string, name string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
