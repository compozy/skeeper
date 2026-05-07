package reconcile

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compozy/skeeper/internal/config"
	"github.com/compozy/skeeper/internal/gitexec"
)

func TestPlanSyncBuildsOwnedNamespacesAndHonorsExcludes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Namespaces: []config.Namespace{
			{Name: "skills", Patterns: []string{"skills/*.md"}},
			{Name: "repo", Patterns: []string{"**/*.md"}, Exclude: []string{"skills/*.md"}},
		},
	})
	writeFile(t, root, "skills/review.md", "# Skill\n")
	writeFile(t, root, "docs/SPEC.md", "# Spec\n")

	plan, err := NewPlanner(&gitexec.ExecRunner{}).PlanSync(ctx, RepoRoot(root), SyncPlanOptions{})
	if err != nil {
		t.Fatalf("plan sync: %v", err)
	}

	if plan.Kind != PlanKindSync || plan.SourceBranch != "main" {
		t.Fatalf("unexpected plan metadata: %#v", plan)
	}
	assertNamespaceFiles(t, plan, "skills", []string{"skills/review.md"})
	assertNamespaceFiles(t, plan, "repo", []string{"docs/SPEC.md"})
}

func TestPlanSyncRespectsGitignoreOverride(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	respectGitignore := false
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Namespaces: []config.Namespace{
			{
				Name:             "repo",
				Patterns:         []string{"ignored/*.md"},
				RespectGitignore: &respectGitignore,
			},
		},
	})
	writeFile(t, root, ".gitignore", "ignored/\n")
	writeFile(t, root, "ignored/SPEC.md", "# Ignored but owned\n")

	plan, err := NewPlanner(&gitexec.ExecRunner{}).PlanSync(ctx, RepoRoot(root), SyncPlanOptions{})
	if err != nil {
		t.Fatalf("plan sync: %v", err)
	}
	assertNamespaceFiles(t, plan, "repo", []string{"ignored/SPEC.md"})
}

func TestPlanSyncUsesStagedBlobContent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"**/SPEC.md"}},
		},
	})
	writeFile(t, root, "src/auth/SPEC.md", "# Staged\n")
	git(t, root, "add", "src/auth/SPEC.md")
	writeFile(t, root, "src/auth/SPEC.md", "# Worktree\n")

	plan, err := NewPlanner(&gitexec.ExecRunner{}).PlanSync(ctx, RepoRoot(root), SyncPlanOptions{Staged: true})
	if err != nil {
		t.Fatalf("plan staged sync: %v", err)
	}
	namespace, ok := planNamespace(plan, "repo")
	if !ok {
		t.Fatalf("missing repo namespace: %#v", plan.Namespaces)
	}
	if got := namespace.StagedContent["src/auth/SPEC.md"]; got != "# Staged\n" {
		t.Fatalf("staged content mismatch: got %q", got)
	}
	if len(namespace.Files) != 1 || !namespace.Files[0].Staged || namespace.Files[0].Size != int64(len("# Staged\n")) {
		t.Fatalf("unexpected staged file plan: %#v", namespace.Files)
	}
}

func TestPlanSyncGuardrailsRequireForce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Settings: config.Settings{
			Guardrails: config.GuardrailSettings{MaxFiles: 1, MaxBytes: 1024},
		},
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"**/*.md"}},
		},
	})
	writeFile(t, root, "a.md", "a\n")
	writeFile(t, root, "b.md", "b\n")

	planner := NewPlanner(&gitexec.ExecRunner{})
	_, err := planner.PlanSync(ctx, RepoRoot(root), SyncPlanOptions{})
	if err == nil || !strings.Contains(err.Error(), "rerun with --force") {
		t.Fatalf("expected guardrail error, got %v", err)
	}
	plan, err := planner.PlanSync(ctx, RepoRoot(root), SyncPlanOptions{Force: true})
	if err != nil {
		t.Fatalf("forced plan sync: %v", err)
	}
	if !plan.Guardrails.RequiresForce || plan.Guardrails.Files != 2 {
		t.Fatalf("unexpected guardrail report: %#v", plan.Guardrails)
	}
}

func TestPlanAdoptRejectsUnsafeOrInvalidTargets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"**/SPEC.md"}},
		},
	})
	writeFile(t, root, "src/auth/SPEC.md", "# Auth\n")
	writeFile(t, root, "src/billing/SPEC.md", "# Billing\n")
	writeFile(t, root, "docs/README.md", "# Docs\n")
	git(t, root, "add", "src/auth/SPEC.md")

	planner := NewPlanner(&gitexec.ExecRunner{})
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "traversal", target: "../outside/SPEC.md", want: "outside the project root"},
		{name: "not owned", target: "docs/README.md", want: "not owned by any skeeper namespace"},
		{name: "untracked", target: "src/billing/SPEC.md", want: "not tracked by git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planner.PlanAdopt(ctx, RepoRoot(root), []string{tt.target}, AdoptPlanOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got %v", tt.want, err)
			}
		})
	}
}

func TestPlanJSONShapeOmitsRuntimeConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := newRepo(t)
	saveConfig(t, root, config.Config{
		Sidecar: "git@example.com:org/specs.git",
		Namespaces: []config.Namespace{
			{Name: "repo", Patterns: []string{"**/*.md"}},
		},
	})
	writeFile(t, root, "docs/SPEC.md", "# Spec\n")

	plan, err := NewPlanner(&gitexec.ExecRunner{}).PlanSync(ctx, RepoRoot(root), SyncPlanOptions{})
	if err != nil {
		t.Fatalf("plan sync: %v", err)
	}
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	text := string(data)
	for _, want := range []string{`"kind":"sync"`, `"source_branch":"main"`, `"namespaces"`, `"guardrails"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("plan JSON missing %s: %s", want, text)
		}
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatalf("unmarshal plan JSON: %v", err)
	}
	if _, ok := shape["Config"]; ok {
		t.Fatalf("plan JSON leaked config field: %s", text)
	}
	if _, ok := shape["StagedContent"]; ok {
		t.Fatalf("plan JSON leaked staged content field: %s", text)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git(t, root, "init", "--initial-branch=main")
	return root
}

func saveConfig(t *testing.T, root string, cfg config.Config) {
	t.Helper()
	if err := config.Save(root, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func assertNamespaceFiles(t *testing.T, plan Plan, name string, want []string) {
	t.Helper()
	namespace, ok := planNamespace(plan, name)
	if !ok {
		t.Fatalf("missing namespace %q: %#v", name, plan.Namespaces)
	}
	got := make([]string, 0, len(namespace.Files))
	for _, file := range namespace.Files {
		got = append(got, file.Path)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("namespace %s files mismatch: got %#v want %#v", name, got, want)
	}
}

func planNamespace(plan Plan, name string) (NamespacePlan, bool) {
	for _, namespace := range plan.Namespaces {
		if string(namespace.Name) == name {
			return namespace, true
		}
	}
	return NamespacePlan{}, false
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
