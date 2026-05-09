package preview

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"github.com/tobiash/flux-manifest-preview/pkg/sops"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// loadRepoResult holds the result of loading and expanding a repo.
type loadRepoResult struct {
	render   *render.Render
	errors   []error
	warnings []error
}

type repoLoader struct {
	preview *Preview
	root    string
	cluster string
	paths   []string
	render  *render.Render
	fs      filesys.FileSystem
	log     logr.Logger
}

func (p *Preview) loadRepo(ctx context.Context, path string) (map[string]*loadRepoResult, error) {
	if p.isClustered() {
		results := make(map[string]*loadRepoResult, len(p.clusterPaths))
		for cluster, paths := range p.clusterPaths {
			result, err := p.loadRepoPaths(ctx, path, paths, cluster)
			if err != nil {
				return nil, err
			}
			results[cluster] = result
		}
		return results, nil
	}

	result, err := p.loadRepoPaths(ctx, path, p.paths, "")
	if err != nil {
		return nil, err
	}
	return map[string]*loadRepoResult{"": result}, nil
}

func (p *Preview) loadRepoPaths(ctx context.Context, path string, paths []string, cluster string) (*loadRepoResult, error) {
	log := p.log.WithValues("renderPath", path)
	if cluster != "" {
		log = log.WithValues("cluster", cluster)
	}

	loader := repoLoader{
		preview: p,
		root:    path,
		cluster: cluster,
		paths:   paths,
		render:  render.NewDefaultRender(log),
		fs:      filesys.MakeFsOnDisk(),
		log:     log,
	}

	initial := make([]expander.DiscoveredPath, len(paths))
	for i, path := range paths {
		initial[i] = expander.DiscoveredPath{Path: path, Producer: fmt.Sprintf("path %s", path)}
	}

	discovery := expander.DiscoveryRunner{
		Expanders:  p.expandersForSource(path),
		PathKey:    loader.pathKey,
		RenderPath: loader.renderPath,
	}
	expanded, err := discovery.Run(ctx, loader.render, initial)
	if err != nil {
		return nil, loader.clusterErrorf("%w", err)
	}

	if p.filters != nil {
		for _, filter := range p.filters.Filters {
			if err := loader.render.ApplyFilter(filter.Filter); err != nil {
				return nil, err
			}
		}
	}

	if p.sopsDecrypt {
		if err := sops.DecryptResources(loader.render); err != nil {
			return nil, loader.clusterErrorf("sops decryption failed: %w", err)
		}
	}

	return &loadRepoResult{
		render:   loader.render,
		errors:   expanded.Errors,
		warnings: loader.render.Warnings(),
	}, nil
}

func (l repoLoader) renderPath(path expander.DiscoveredPath) error {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = l.root
	}
	full := filepath.Join(baseDir, path.Path)
	if !l.fs.Exists(full) {
		if l.userPath(path.Path) {
			return fmt.Errorf("path %q does not exist", path.Path)
		}
		l.log.V(1).Info("skipping non-existent path", "path", path.Path)
		return nil
	}

	l.log.V(1).Info("rendering path", "path", path.Path, "baseDir", path.BaseDir)
	count := l.render.Size()
	producer := path.Producer
	if producer == "" {
		producer = fmt.Sprintf("path %s", path.Path)
	}
	if l.preview.recursive {
		if err := l.render.AddPathsWithProducer(l.fs, full, producer); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	} else {
		if err := l.render.AddPathWithProducer(l.fs, full, producer); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	}
	if path.Namespace != "" {
		l.render.ApplyNamespaceToNew(count, path.Namespace)
	}
	l.render.MarkProvenanceToNew(count, producer)
	return nil
}

func (l repoLoader) pathKey(path expander.DiscoveredPath) string {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = l.root
	}
	return filepath.Join(baseDir, path.Path)
}

func (l repoLoader) userPath(path string) bool {
	for _, userPath := range l.paths {
		if userPath == path {
			return true
		}
	}
	return false
}

func (l repoLoader) clusterErrorf(format string, args ...any) error {
	if l.cluster != "" {
		return fmt.Errorf("cluster %q: "+format, append([]any{l.cluster}, args...)...)
	}
	return fmt.Errorf(format, args...)
}
