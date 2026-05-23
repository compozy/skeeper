// Package gitexec provides context-aware shell execution for git and gh.
package gitexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Result captures process output streams.
type Result struct {
	Stdout string
	Stderr string
}

// Runner executes external commands in a working directory.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (Result, error)
	RunWithInput(ctx context.Context, dir, stdin, name string, args ...string) (Result, error)
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

var _ Runner = (*ExecRunner)(nil)

var gitRepositoryEnv = map[string]struct{}{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_COMMON_DIR":                   {},
	"GIT_DIR":                          {},
	"GIT_GRAFT_FILE":                   {},
	"GIT_IMPLICIT_WORK_TREE":           {},
	"GIT_INDEX_FILE":                   {},
	"GIT_NAMESPACE":                    {},
	"GIT_NO_REPLACE_OBJECTS":           {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_PREFIX":                       {},
	"GIT_REPLACE_REF_BASE":             {},
	"GIT_SHALLOW_FILE":                 {},
	"GIT_WORK_TREE":                    {},
}

// Run executes name with args in dir and returns stdout and stderr.
func (r *ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (Result, error) {
	return r.RunWithInput(ctx, dir, "", name, args...)
}

// RunWithInput executes name with args and writes stdin to the process.
func (r *ExecRunner) RunWithInput(
	ctx context.Context,
	dir string,
	stdin string,
	name string,
	args ...string,
) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if isGitCommand(name) {
		cmd.Env = withoutGitRepositoryEnv(os.Environ())
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return result, &CommandError{
			Name:     name,
			Args:     append([]string(nil), args...),
			Dir:      dir,
			Stderr:   strings.TrimSpace(result.Stderr),
			ExitCode: exitCode,
			Err:      err,
		}
	}
	return result, nil
}

func isGitCommand(name string) bool {
	base := filepath.Base(name)
	return base == "git" || strings.EqualFold(base, "git.exe")
}

func withoutGitRepositoryEnv(env []string) []string {
	cleaned := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, blocked := gitRepositoryEnv[key]; blocked {
				continue
			}
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

// CommandError wraps an external command failure with useful context.
type CommandError struct {
	Name   string
	Args   []string
	Dir    string
	Stderr string
	// ExitCode is the process exit code, or -1 when unavailable.
	ExitCode int
	Err      error
}

// Error returns a concise command failure message.
func (e *CommandError) Error() string {
	command := strings.TrimSpace(e.Name + " " + strings.Join(e.Args, " "))
	if e.Stderr != "" {
		return fmt.Sprintf("%s in %s: %s: %v", command, e.Dir, e.Stderr, e.Err)
	}
	return fmt.Sprintf("%s in %s: %v", command, e.Dir, e.Err)
}

// Unwrap returns the underlying process error.
func (e *CommandError) Unwrap() error {
	return e.Err
}

// IsDeadline reports whether err is caused by context timeout or cancellation.
func IsDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// TrimmedStdout returns stdout without surrounding whitespace.
func TrimmedStdout(result Result) string {
	return strings.TrimSpace(result.Stdout)
}
