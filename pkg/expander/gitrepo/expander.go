package gitrepo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

var gitRepoGVK = resid.NewGvk("source.toolkit.fluxcd.io", "v1", "GitRepository")

// Expander discovers GitRepository resources, clones external repos to temp
// directories, and makes their paths available for the expansion loop.
type Expander struct {
	log       logr.Logger
	clones    cloneCache
	cloneDir  string // parent directory for clones
	cleanup   func()
}

type cloneCache struct {
	mu    sync.Mutex
	paths map[string]string // "namespace/name" -> local path
}

// NewExpander creates a GitRepository expander.
// The cleanup function removes cloned repos and must be called when done.
func NewExpander(log logr.Logger) (*Expander, error) {
	tmpDir, err := os.MkdirTemp("", "fmp-gitrepo-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	return &Expander{
		log:      log,
		cloneDir: tmpDir,
		cleanup: func() { os.RemoveAll(tmpDir) },
		clones: cloneCache{
			paths: make(map[string]string),
		},
	}, nil
}

// Cleanup removes all cloned repositories.
func (e *Expander) Cleanup() {
	if e.cleanup != nil {
		e.cleanup()
		e.cleanup = nil
	}
}

// Expand implements expander.Expander.
func (e *Expander) Expand(_ context.Context, r *render.Render) (*expander.ExpandResult, error) {
	type gitRepoInfo struct {
		url string
		ref string // branch or tag
	}

	// Collect GitRepository resources.
	repos := make(map[string]gitRepoInfo) // "namespace/name" -> info
	for _, res := range r.Resources() {
		gvk := res.GetGvk()
		if gvk.Group != gitRepoGVK.Group || gvk.Kind != gitRepoGVK.Kind {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		spec, ok := m["spec"].(map[string]any)
		if !ok {
			continue
		}
		url, _, _ := unstructured.NestedString(spec, "url")
		if url == "" {
			continue
		}
		// Extract ref: prefer branch, then tag.
		ref := extractRef(spec)
		key := res.GetNamespace() + "/" + res.GetName()
		repos[key] = gitRepoInfo{url: url, ref: ref}
	}

	if len(repos) == 0 {
		return &expander.ExpandResult{}, nil
	}

	// Clone each unique repo URL.
	e.clones.mu.Lock()
	defer e.clones.mu.Unlock()

	for key, info := range repos {
		if _, exists := e.clones.paths[key]; exists {
			continue
		}

		// Only clone non-local URLs. file:// and local paths are skipped.
		if isLocalURL(info.url) {
			localPath := stripFilePrefix(info.url)
			e.log.Info("using local GitRepository", "key", key, "path", localPath)
			e.clones.paths[key] = localPath
			continue
		}

		clonePath := filepath.Join(e.cloneDir, key)
		if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
			return nil, fmt.Errorf("creating clone directory: %w", err)
		}

		e.log.Info("cloning GitRepository", "key", key, "url", info.url, "ref", info.ref)
		if err := gitClone(info.url, clonePath, info.ref); err != nil {
			e.log.Error(err, "failed to clone GitRepository, skipping", "key", key, "url", info.url)
			continue
		}

		e.clones.paths[key] = clonePath
	}

	return &expander.ExpandResult{}, nil
}

// ResolvePath returns the local filesystem path for a GitRepository source.
// Returns ("", false) if the repository hasn't been cloned.
func (e *Expander) ResolvePath(namespace, name string) (string, bool) {
	e.clones.mu.Lock()
	defer e.clones.mu.Unlock()
	key := namespace + "/" + name
	path, ok := e.clones.paths[key]
	return path, ok
}

func extractRef(spec map[string]any) string {
	ref, ok := spec["ref"].(map[string]any)
	if !ok {
		return ""
	}
	if branch, _, _ := unstructured.NestedString(ref, "branch"); branch != "" {
		return branch
	}
	if tag, _, _ := unstructured.NestedString(ref, "tag"); tag != "" {
		return tag
	}
	return ""
}

func gitClone(url, dest, ref string) error {
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, dest)
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone %s: %s: %w", url, string(out), err)
	}
	return nil
}

func isLocalURL(url string) bool {
	return filepath.IsAbs(url) || len(url) > 5 && url[:5] == "file:"
}

func stripFilePrefix(url string) string {
	if len(url) > 5 && url[:5] == "file://" {
		return url[7:]
	}
	if len(url) > 5 && url[:5] == "file:" {
		return url[5:]
	}
	return url
}
