package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gitrepository "github.com/fluxcd/pkg/git/repository"
	"github.com/go-logr/logr"
)

func TestSourceRootsKeepDistinctAcquisitions(t *testing.T) {
	for _, change := range []struct{ name, url, ref, extra string }{
		{"url", "https://example.com/other", "main", ""},
		{"ref", "https://example.com/repo", "target", ""},
		{"sparse checkout", "https://example.com/repo", "main", "  sparseCheckout:\n  - charts\n"},
		{"submodules", "https://example.com/repo", "main", "  recurseSubmodules: true\n"},
		{"commit with branch", "https://example.com/repo", "main", "    commit: abc123\n"},
	} {
		t.Run(change.name, func(t *testing.T) {
			e, err := NewExpander(logr.Discard())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(e.Cleanup)
			original := gitCloneFunc
			t.Cleanup(func() { gitCloneFunc = original })
			gitCloneFunc = func(_ context.Context, url, dest string, cfg gitrepository.CloneConfig) error {
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dest, "identity"), []byte(url+"@"+cfg.Branch), 0o644)
			}
			scopes := []*Expander{e.WithSourceRoot(t.TempDir()), e.WithSourceRoot(t.TempDir())}
			urls := []string{"https://example.com/repo", change.url}
			refs := []string{"main", change.ref}
			var wg sync.WaitGroup
			for i, scoped := range scopes {
				manifest := fmt.Sprintf("apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: app\n  namespace: flux-system\nspec:\n  url: %s\n  ref:\n    branch: %s\n", urls[i], refs[i])
				if i == 1 {
					manifest += change.extra
				}
				r := newRenderFromYAML(t, manifest)
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := scoped.Expand(context.Background(), r)
					if err != nil {
						t.Error(err)
						return
					}
					if len(result.Errors) != 0 {
						t.Errorf("Expand errors = %v", result.Errors)
					}
				}()
			}
			wg.Wait()
			first, _ := scopes[0].ResolvePath("flux-system", "app")
			second, _ := scopes[1].ResolvePath("flux-system", "app")
			if first == second {
				t.Errorf("different acquisition configurations share path %q", first)
			}
			for i, scoped := range scopes {
				path, ok := scoped.ResolvePath("flux-system", "app")
				if !ok {
					t.Fatal("ResolvePath missing acquisition")
				}
				data, err := os.ReadFile(filepath.Join(path, "identity"))
				if err != nil {
					t.Fatal(err)
				}
				if want := urls[i] + "@" + refs[i]; string(data) != want {
					t.Errorf("source %d identity = %q, want %q", i, data, want)
				}
			}
		})
	}
}

func TestExpansionFailuresAreReported(t *testing.T) {
	original := gitCloneFunc
	t.Cleanup(func() { gitCloneFunc = original })
	gitCloneFunc = func(context.Context, string, string, gitrepository.CloneConfig) error {
		return errors.New("clone failed")
	}
	for _, spec := range []string{"url: https://example.com/repo", "url: https://example.com/repo\n  sparseCheckout: invalid", "url: https://example.com/repo\n  ref:\n    branch: 42", "url: https://example.com/repo\n  ref: invalid", "url: 42"} {
		e, err := NewExpander(logr.Discard())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Cleanup)
		r := newRenderFromYAML(t, "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: app\nspec:\n  "+spec+"\n")
		result, err := e.Expand(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) == 0 {
			t.Errorf("Expand(%q) errors empty, want acquisition/spec error", spec)
		}
	}
}
