package gitrepo

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	fluxgit "github.com/fluxcd/pkg/git"
	fluxgogit "github.com/fluxcd/pkg/git/gogit"
	gitrepository "github.com/fluxcd/pkg/git/repository"
	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

var gitRepoGVK = resid.NewGvk("source.toolkit.fluxcd.io", "v1", "GitRepository")

type cloneClient interface {
	Clone(ctx context.Context, url string, cfg gitrepository.CloneConfig) (*fluxgit.Commit, error)
	Close()
}

// Expander discovers GitRepository resources, clones external repos to temp
// directories, and makes their paths available for the expansion loop.
type Expander struct {
	log            logr.Logger
	shared         *sharedState
	localPaths     map[string]string
	sourceRoot     string
	sourceRepoURLs map[string]struct{}
}

type sharedState struct {
	clones   cloneCache
	cloneDir string // parent directory for clones
	cleanup  func()
	group    singleflight.Group
}

type cloneCache struct {
	mu    sync.Mutex
	paths map[string]string // "namespace/name" -> local path
}

const sourceRepoURLsFile = ".fmp-source-repo-urls"

var gitCloneFunc = gitClone

var newCloneClient = func(dest string, authOpts *fluxgit.AuthOptions) (cloneClient, error) {
	return fluxgogit.NewClient(
		dest,
		authOpts,
		fluxgogit.WithDiskStorage(),
		fluxgogit.WithFallbackToDefaultKnownHosts(),
	)
}

// WriteSourceRepoURLs writes normalized source-repo aliases for a materialized tree.
// This lets archived git revision snapshots resolve self-referential GitRepository URLs.
func WriteSourceRepoURLs(path, repoRoot string) error {
	urls := gitRemoteURLs(repoRoot)
	if len(urls) == 0 {
		return nil
	}
	data := strings.Join(urls, "\n") + "\n"
	return os.WriteFile(filepath.Join(path, sourceRepoURLsFile), []byte(data), 0o644)
}

// NewExpander creates a GitRepository expander.
// The cleanup function removes cloned repos and must be called when done.
func NewExpander(log logr.Logger) (*Expander, error) {
	tmpDir, err := os.MkdirTemp("", "fmp-gitrepo-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	return &Expander{
		log: log,
		shared: &sharedState{
			cloneDir: tmpDir,
			cleanup:  func() { _ = os.RemoveAll(tmpDir) },
			clones: cloneCache{
				paths: make(map[string]string),
			},
		},
		localPaths: make(map[string]string),
	}, nil
}

// Cleanup removes all cloned repositories.
func (e *Expander) Cleanup() {
	if e.shared != nil && e.shared.cleanup != nil {
		e.shared.cleanup()
		e.shared.cleanup = nil
	}
}

// WithSourceRoot returns a copy of the expander scoped to one source root.
// External clones remain shared, while local path aliases are per invocation.
func (e *Expander) WithSourceRoot(path string) *Expander {
	clone := &Expander{
		log:        e.log,
		shared:     e.shared,
		localPaths: make(map[string]string),
		sourceRoot: path,
	}
	clone.sourceRepoURLs = discoverSourceRepoURLs(path)
	return clone
}

// Expand implements expander.Expander.
func (e *Expander) Expand(ctx context.Context, r *render.Render) (*expander.ExpandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	type gitRepoInfo struct {
		url   string
		clone gitrepository.CloneConfig
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
		key := res.GetNamespace() + "/" + res.GetName()
		cloneCfg, err := cloneConfigForSpec(spec)
		if err != nil {
			e.log.Error(err, "failed to parse GitRepository ref, skipping", "key", key, "url", url)
			continue
		}
		repos[key] = gitRepoInfo{url: url, clone: cloneCfg}
	}

	if len(repos) == 0 {
		return &expander.ExpandResult{}, nil
	}

	for key, info := range repos {
		if _, exists := e.localPaths[key]; exists {
			continue
		}
		if _, exists := e.sharedClonePath(key); exists {
			continue
		}

		if e.matchesCurrentSource(info.url) {
			e.log.V(1).Info("using current repo for GitRepository", "key", key, "path", e.sourceRoot)
			e.localPaths[key] = e.sourceRoot
			continue
		}

		// Only clone non-local URLs. file:// and local paths are skipped.
		if isLocalURL(info.url) {
			localPath := stripFilePrefix(info.url)
			e.log.V(1).Info("using local GitRepository", "key", key, "path", localPath)
			e.localPaths[key] = localPath
			continue
		}

		if err := e.ensureSharedClone(ctx, key, info.url, info.clone); err != nil {
			e.log.Error(err, "failed to clone GitRepository, skipping", "key", key, "url", info.url)
			continue
		}
	}

	return &expander.ExpandResult{}, nil
}

// ResolvePath returns the local filesystem path for a GitRepository source.
// Returns ("", false) if the repository hasn't been cloned.
func (e *Expander) ResolvePath(namespace, name string) (string, bool) {
	if e == nil {
		return "", false
	}
	key := namespace + "/" + name
	if path, ok := e.localPaths[key]; ok {
		return path, true
	}
	return e.sharedClonePath(key)
}

func (e *Expander) matchesCurrentSource(rawURL string) bool {
	if e.sourceRoot == "" || len(e.sourceRepoURLs) == 0 {
		return false
	}
	normalized, ok := normalizeGitURL(rawURL)
	if !ok {
		return false
	}
	_, exists := e.sourceRepoURLs[normalized]
	return exists
}

func (e *Expander) sharedClonePath(key string) (string, bool) {
	e.shared.clones.mu.Lock()
	defer e.shared.clones.mu.Unlock()
	path, ok := e.shared.clones.paths[key]
	return path, ok
}

func (e *Expander) setSharedClonePath(key, path string) {
	e.shared.clones.mu.Lock()
	defer e.shared.clones.mu.Unlock()
	e.shared.clones.paths[key] = path
}

func (e *Expander) ensureSharedClone(ctx context.Context, key, rawURL string, cloneCfg gitrepository.CloneConfig) error {
	if _, exists := e.sharedClonePath(key); exists {
		return nil
	}

	_, err, _ := e.shared.group.Do(key, func() (any, error) {
		if _, exists := e.sharedClonePath(key); exists {
			return nil, nil
		}

		clonePath := filepath.Join(e.shared.cloneDir, key)
		if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
			return nil, fmt.Errorf("creating clone directory: %w", err)
		}

		e.log.V(1).Info("cloning GitRepository", "key", key, "url", rawURL, "ref", describeCloneConfig(cloneCfg))
		if err := gitCloneFunc(ctx, rawURL, clonePath, cloneCfg); err != nil {
			return nil, err
		}

		e.setSharedClonePath(key, clonePath)
		return nil, nil
	})
	return err
}

func cloneConfigForSpec(spec map[string]any) (gitrepository.CloneConfig, error) {
	ref, ok := spec["ref"].(map[string]any)
	if !ok {
		ref = nil
	}
	cloneCfg := gitrepository.CloneConfig{
		CheckoutStrategy:  gitrepository.CheckoutStrategy{},
		RecurseSubmodules: nestedBool(spec, "recurseSubmodules"),
		ShallowClone:      true,
	}
	if branch, _, _ := unstructured.NestedString(ref, "branch"); branch != "" {
		cloneCfg.Branch = branch
	}
	if tag, _, _ := unstructured.NestedString(ref, "tag"); tag != "" {
		cloneCfg.Tag = tag
	}
	if semver, _, _ := unstructured.NestedString(ref, "semver"); semver != "" {
		cloneCfg.SemVer = semver
	}
	if name, _, _ := unstructured.NestedString(ref, "name"); name != "" {
		cloneCfg.RefName = name
	}
	if commit, _, _ := unstructured.NestedString(ref, "commit"); commit != "" {
		cloneCfg.Commit = commit
	}
	if sparseCheckout, ok, err := unstructured.NestedStringSlice(spec, "sparseCheckout"); err != nil {
		return gitrepository.CloneConfig{}, err
	} else if ok {
		cloneCfg.SparseCheckoutDirectories = append([]string(nil), sparseCheckout...)
	}
	return cloneCfg, nil
}

func gitClone(ctx context.Context, rawURL, dest string, cloneCfg gitrepository.CloneConfig) error {
	if ctx == nil {
		ctx = context.Background()
	}
	authOpts, err := authOptionsForURL(rawURL)
	if err != nil {
		return err
	}
	client, err := newCloneClient(dest, authOpts)
	if err != nil {
		return fmt.Errorf("creating git client for %s: %w", rawURL, err)
	}
	defer client.Close()
	if _, err := client.Clone(ctx, rawURL, cloneCfg); err != nil {
		return fmt.Errorf("cloning %s: %w", rawURL, err)
	}
	return nil
}

func authOptionsForURL(rawURL string) (*fluxgit.AuthOptions, error) {
	u, err := parseGitURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing git url %q: %w", rawURL, err)
	}
	if u.Scheme == string(fluxgit.SSH) {
		username := u.User.Username()
		if username == "" {
			username = fluxgit.DefaultPublicKeyAuthUser
		}
		return &fluxgit.AuthOptions{
			Transport: fluxgit.SSH,
			Host:      u.Host,
			Username:  username,
		}, nil
	}
	return fluxgit.NewAuthOptions(*u, nil)
}

func parseGitURL(raw string) (*url.URL, error) {
	if strings.Contains(raw, "://") {
		return url.Parse(raw)
	}
	if at := strings.Index(raw, "@"); at >= 0 {
		remainder := raw[at+1:]
		parts := strings.SplitN(remainder, ":", 2)
		if len(parts) == 2 {
			return &url.URL{
				Scheme: string(fluxgit.SSH),
				User:   url.User(raw[:at]),
				Host:   parts[0],
				Path:   "/" + strings.TrimPrefix(parts[1], "/"),
			}, nil
		}
	}
	return url.Parse(raw)
}

func nestedBool(spec map[string]any, key string) bool {
	v, ok := spec[key].(bool)
	return ok && v
}

func describeCloneConfig(cfg gitrepository.CloneConfig) string {
	switch {
	case cfg.Commit != "":
		return cfg.Commit
	case cfg.RefName != "":
		return cfg.RefName
	case cfg.SemVer != "":
		return cfg.SemVer
	case cfg.Tag != "":
		return cfg.Tag
	case cfg.Branch != "":
		return cfg.Branch
	default:
		return "default"
	}
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

func discoverSourceRepoURLs(path string) map[string]struct{} {
	urls := make(map[string]struct{})
	for _, raw := range append(readSourceRepoURLsFile(path), gitRemoteURLs(path)...) {
		normalized, ok := normalizeGitURL(raw)
		if ok {
			urls[normalized] = struct{}{}
		}
	}
	return urls
}

func readSourceRepoURLsFile(path string) []string {
	data, err := os.ReadFile(filepath.Join(path, sourceRepoURLsFile))
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	urls := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		urls = append(urls, line)
	}
	return urls
}

func gitRemoteURLs(path string) []string {
	out, err := exec.Command("git", "-C", path, "remote").Output()
	if err != nil {
		return nil
	}
	var urls []string
	for _, remote := range strings.Fields(string(out)) {
		remoteOut, err := exec.Command("git", "-C", path, "remote", "get-url", "--all", remote).Output()
		if err != nil {
			continue
		}
		urls = append(urls, strings.Fields(string(remoteOut))...)
	}
	return urls
}

func normalizeGitURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || isLocalURL(trimmed) {
		return "", false
	}
	if strings.Contains(trimmed, "://") {
		u, err := url.Parse(trimmed)
		if err != nil || u.Host == "" {
			return "", false
		}
		host := strings.ToLower(u.Hostname())
		path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if path == "" {
			return "", false
		}
		return host + "/" + path, true
	}
	if at := strings.Index(trimmed, "@"); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) != 2 {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(parts[0]))
	path := strings.TrimSuffix(strings.Trim(strings.TrimSpace(parts[1]), "/"), ".git")
	if host == "" || path == "" {
		return "", false
	}
	return host + "/" + path, true
}
