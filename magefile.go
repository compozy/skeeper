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
	goreleaserVersion     = "v2.15.4"
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

// Build compiles the application binary into bin/skeeper with version ldflags.
func Build() error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(binDir, appBinary)
	return sh.RunV("go", "build", "-trimpath", "-ldflags", buildLDFlags(), "-o", out, "./cmd/skeeper")
}

// Install installs the application binary into GOBIN/GOPATH/bin with version ldflags.
func Install() error {
	installPath, err := goInstallPath()
	if err != nil {
		return err
	}
	fmt.Printf("Installing %s to %s\n", appBinary, installPath)
	if err := sh.RunV("go", "install", "-trimpath", "-ldflags", buildLDFlags(), "./cmd/skeeper"); err != nil {
		return fmt.Errorf("install %s: %w", appBinary, err)
	}
	fmt.Printf("Installed %s to %s\n", appBinary, installPath)
	return nil
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

// ReleaseSnapshot produces a local GoReleaser Pro snapshot under dist/.
func ReleaseSnapshot() error {
	args := []string{"release", "--snapshot", "--clean", "--skip=publish"}
	if _, err := exec.LookPath("goreleaser"); err == nil {
		return sh.RunV("goreleaser", args...)
	}
	if os.Getenv("GORELEASER_KEY") == "" {
		return fmt.Errorf("goreleaser not found in PATH; install GoReleaser Pro or set GORELEASER_KEY to use the official installer")
	}
	command := fmt.Sprintf(
		"curl -sfL https://goreleaser.com/static/run | DISTRIBUTION=pro VERSION=%s bash -s -- %s",
		goreleaserVersion,
		strings.Join(args, " "),
	)
	return sh.RunV("bash", "-c", command)
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

func goInstallPath() (string, error) {
	gobin, err := goEnv("GOBIN")
	if err != nil {
		return "", err
	}
	if gobin != "" {
		return filepath.Join(gobin, appBinary), nil
	}
	gopath, err := goEnv("GOPATH")
	if err != nil {
		return "", err
	}
	if gopath == "" {
		return "", fmt.Errorf("go env GOPATH returned an empty path")
	}
	firstGoPath := filepath.SplitList(gopath)[0]
	if firstGoPath == "" {
		return "", fmt.Errorf("go env GOPATH returned an empty first path")
	}
	return filepath.Join(firstGoPath, "bin", appBinary), nil
}

func goEnv(key string) (string, error) {
	cmd := exec.Command("go", "env", key)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
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
