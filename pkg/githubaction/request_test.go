package githubaction

import (
	"testing"
)

func TestGetInputSupportsUnderscoreAndHyphenEnvKeys(t *testing.T) {
	t.Setenv("INPUT_REPO_A", "/tmp/repo-a")
	t.Setenv("INPUT_REPO-B", "/tmp/repo-b")

	if got := getInput("repo-a", ""); got != "/tmp/repo-a" {
		t.Fatalf("getInput(repo-a) with underscore env = %q, want /tmp/repo-a", got)
	}

	if got := getInput("repo-b", ""); got != "/tmp/repo-b" {
		t.Fatalf("getInput(repo-b) with hyphen env = %q, want /tmp/repo-b", got)
	}
}
