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

func TestParseRequestFromEnvSupportsMultiplePaths(t *testing.T) {
	t.Setenv("INPUT_PATHS", "clusters/kube\nclusters/prod\nclusters/edge")

	req, err := ParseRequestFromEnv()
	if err != nil {
		t.Fatalf("ParseRequestFromEnv() error = %v", err)
	}

	want := []string{"clusters/kube", "clusters/prod", "clusters/edge"}
	if len(req.Paths) != len(want) {
		t.Fatalf("len(req.Paths) = %d, want %d", len(req.Paths), len(want))
	}

	for i := range want {
		if req.Paths[i] != want[i] {
			t.Fatalf("req.Paths[%d] = %q, want %q", i, req.Paths[i], want[i])
		}
	}
}
