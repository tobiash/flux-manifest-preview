package preview

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
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
	if len(paths) == 0 {
		return nil, fmt.Errorf("no render roots configured: specify --path or configure paths in .fmp.yaml")
	}
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

	result, err := (fluxRenderGraph{loader: loader}).Load(ctx)
	if err != nil {
		return nil, err
	}
	// The shipped renderer replaces duplicate IDs and exposes only a warning.
	// Such replacement loses input, unlike intentionally skipped dependencies.
	for _, warning := range result.warnings {
		if strings.HasPrefix(warning.Error(), "duplicate resource ") {
			result.errors = append(result.errors, warning)
		}
	}
	return result, nil
}

func (l repoLoader) clusterErrorf(format string, args ...any) error {
	if l.cluster != "" {
		return fmt.Errorf("cluster %q: "+format, append([]any{l.cluster}, args...)...)
	}
	return fmt.Errorf(format, args...)
}
