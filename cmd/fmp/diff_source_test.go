package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

func TestResolveDiffPlan_DefaultsToHeadVsWorktree(t *testing.T) {
	repo := initGitRepo(t)
	commitFile(t, repo, "kustomization.yaml", "resources:\n- configmap.yaml\n", "init")
	commitFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: old\n", "add config")

	plan, err := resolveDiffPlan(nil, repo)
	if err != nil {
		t.Fatalf("resolveDiffPlan() error = %v", err)
	}
	if plan.configRoot != repo {
		t.Fatalf("configRoot = %q, want %q", plan.configRoot, repo)
	}
	if plan.left.kind != diffSourceRevision || plan.left.raw != "HEAD" {
		t.Fatalf("left = %+v, want HEAD revision", plan.left)
	}
	if plan.right.kind != diffSourceWorktree || plan.right.repoRoot != repo {
		t.Fatalf("right = %+v, want worktree rooted at %q", plan.right, repo)
	}
}

func TestResolveDiffPlan_TwoPathsPreferPaths(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	plan, err := resolveDiffPlan([]string{left, right}, t.TempDir())
	if err != nil {
		t.Fatalf("resolveDiffPlan() error = %v", err)
	}
	if plan.left.kind != diffSourcePath || plan.right.kind != diffSourcePath {
		t.Fatalf("expected path sources, got left=%q right=%q", plan.left.kind, plan.right.kind)
	}
	if plan.configRoot != left {
		t.Fatalf("configRoot = %q, want %q", plan.configRoot, left)
	}
}

func TestResolveDiffPlan_TwoRevsUseGit(t *testing.T) {
	repo := initGitRepo(t)
	commitFile(t, repo, "kustomization.yaml", "resources:\n- configmap.yaml\n", "init")
	commitFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: old\n", "add config")
	commitFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: new\n", "update config")

	plan, err := resolveDiffPlan([]string{"HEAD~1", "HEAD"}, repo)
	if err != nil {
		t.Fatalf("resolveDiffPlan() error = %v", err)
	}
	if plan.left.kind != diffSourceRevision || plan.right.kind != diffSourceRevision {
		t.Fatalf("expected revision sources, got left=%q right=%q", plan.left.kind, plan.right.kind)
	}
	if plan.configRoot != repo {
		t.Fatalf("configRoot = %q, want %q", plan.configRoot, repo)
	}
}

func TestResolveDiffPlan_MixedImplicitSourcesRequirePrefixes(t *testing.T) {
	repo := initGitRepo(t)
	commitFile(t, repo, "file.txt", "hello\n", "init")
	other := t.TempDir()

	_, err := resolveDiffPlan([]string{"HEAD", other}, repo)
	if err == nil {
		t.Fatal("expected mixed implicit sources to fail")
	}
	if !strings.Contains(err.Error(), "explicit git: or path: prefixes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMaterializeRevision_CapturesCommittedState(t *testing.T) {
	repo := initGitRepo(t)
	commitFile(t, repo, "tracked.txt", "old\n", "add tracked")
	writeFile(t, repo, "tracked.txt", "new\n")

	src := diffSource{kind: diffSourceRevision, raw: "HEAD", repoRoot: repo}
	path, cleanup, err := src.materialize(context.Background())
	if err != nil {
		t.Fatalf("materialize() error = %v", err)
	}
	defer cleanup()

	data, err := os.ReadFile(filepath.Join(path, "tracked.txt"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(data) != "old\n" {
		t.Fatalf("materialized file = %q, want %q", string(data), "old\n")
	}
}

func TestRunDiff_NoArgsComparesHeadToWorktree(t *testing.T) {
	resetGlobals()
	repo := initGitRepo(t)
	writeFile(t, repo, ".fmp.yaml", "paths:\n  - .\nsort: true\n")
	writeFile(t, repo, "kustomization.yaml", "resources:\n- configmap.yaml\n")
	writeFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: old\n")
	gitRun(t, repo, "add", ".")
	gitCommit(t, repo, "initial state")
	writeFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: new\n")

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(origWD)
	}()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	var summary bytes.Buffer
	var diffOut bytes.Buffer
	if err := runDiff(logr.Discard(), nil, &summary, &diffOut); err != nil {
		t.Fatalf("runDiff() error = %v", err)
	}
	if !strings.Contains(summary.String(), "Mostly in-place changes detected.") {
		t.Fatalf("expected summary classification, got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "🟢 0 to add, 🟡 1 to change, 🔴 0 to destroy.") {
		t.Fatalf("expected terraform-style summary, got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "KIND") || !strings.Contains(summary.String(), "ADDED") || !strings.Contains(summary.String(), "TOTAL") {
		t.Fatalf("expected kind table header, got:\n%s", summary.String())
	}
	result := diffOut.String()
	if !strings.Contains(result, "value: old") || !strings.Contains(result, "value: new") {
		t.Fatalf("expected diff to mention old and new values, got:\n%s", result)
	}
}

func TestRunDiff_NoArgsResolveGitReusesCurrentRepo(t *testing.T) {
	resetGlobals()
	resolveGit = true
	repo := initGitRepo(t)
	gitRun(t, repo, "remote", "add", "origin", "https://github.com/tobiash/kube.git")
	writeFile(t, repo, ".fmp.yaml", "paths:\n  - .\nsort: true\n")
	writeFile(t, repo, "sources.yaml", `apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: flux-system
  namespace: flux-system
spec:
  url: ssh://git@github.com/tobiash/kube.git
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: app
  namespace: flux-system
spec:
  path: ./app
  sourceRef:
    kind: GitRepository
    name: flux-system
`)
	writeFile(t, repo, "app/configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: old\n")
	gitRun(t, repo, "add", ".")
	gitCommit(t, repo, "initial state")
	writeFile(t, repo, "app/configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: new\n")

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() {
		_ = os.Chdir(origWD)
	}()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	var summary bytes.Buffer
	var diffOut bytes.Buffer
	if err := runDiff(logr.Discard(), nil, &summary, &diffOut); err != nil {
		t.Fatalf("runDiff() error = %v", err)
	}
	if !strings.Contains(summary.String(), "Mostly in-place changes detected.") {
		t.Fatalf("expected summary classification, got:\n%s", summary.String())
	}
	result := diffOut.String()
	if !strings.Contains(result, "value: old") || !strings.Contains(result, "value: new") {
		t.Fatalf("expected diff to mention old and new values, got:\n%s", result)
	}
}

func TestRunDiff_FailsOnPolicy(t *testing.T) {
	resetGlobals()
	repo := initGitRepo(t)
	writeFile(t, repo, ".fmp.yaml", `paths:
  - .
sort: true
policies:
  inline:
    - |
      package fmp
      import rego.v1

      violations contains {
        "id": "block_configmap",
        "message": "ConfigMap changes are blocked"
      } if {
        some change in input.changes
        change.kind == "ConfigMap"
      }
  fail-on:
    - block_configmap
`)
	writeFile(t, repo, "kustomization.yaml", "resources:\n- configmap.yaml\n")
	writeFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: old\n")
	gitRun(t, repo, "add", ".")
	gitCommit(t, repo, "initial state")
	writeFile(t, repo, "configmap.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\ndata:\n  value: new\n")

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}

	var summary bytes.Buffer
	var diffOut bytes.Buffer
	err = runDiff(logr.Discard(), nil, &summary, &diffOut)
	if err == nil {
		t.Fatal("expected policy failure")
	}
	if !strings.Contains(err.Error(), "policy enforcement failed: block_configmap") {
		t.Fatalf("expected policy failure error, got %v", err)
	}
	if !strings.Contains(summary.String(), "Violations:") || !strings.Contains(summary.String(), "block_configmap") {
		t.Fatalf("expected policy violation summary, got:\n%s", summary.String())
	}
	if !strings.Contains(summary.String(), "Policy enforcement failed:") {
		t.Fatalf("expected policy failure section, got:\n%s", summary.String())
	}
	if !strings.Contains(diffOut.String(), "value: old") || !strings.Contains(diffOut.String(), "value: new") {
		t.Fatalf("expected diff output despite policy failure, got:\n%s", diffOut.String())
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init")
	return repo
}

func commitFile(t *testing.T, repo, name, content, message string) {
	t.Helper()
	writeFile(t, repo, name, content)
	gitRun(t, repo, "add", name)
	gitCommit(t, repo, message)
}

func gitCommit(t *testing.T, repo, message string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "-c", "user.name=Test User", "-c", "user.email=test@example.com", "commit", "-m", message)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit %q failed: %v\n%s", message, err, string(out))
	}
}

func gitRun(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
