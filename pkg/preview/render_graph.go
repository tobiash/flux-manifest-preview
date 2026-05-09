package preview

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/sops"
)

type fluxRenderGraph struct {
	loader repoLoader
}

func (g fluxRenderGraph) Load(ctx context.Context) (*loadRepoResult, error) {
	discovery := expander.DiscoveryRunner{
		Expanders:  g.loader.preview.expandersForSource(g.loader.root),
		PathKey:    g.pathKey,
		RenderPath: g.renderPath,
	}
	expanded, err := discovery.Run(ctx, g.loader.render, g.initialPaths())
	if err != nil {
		return nil, g.loader.clusterErrorf("%w", err)
	}

	if err := g.applyFilters(); err != nil {
		return nil, err
	}
	if err := g.decryptSOPS(); err != nil {
		return nil, err
	}

	return &loadRepoResult{
		render:   g.loader.render,
		errors:   expanded.Errors,
		warnings: g.loader.render.Warnings(),
	}, nil
}

func (g fluxRenderGraph) initialPaths() []expander.DiscoveredPath {
	initial := make([]expander.DiscoveredPath, len(g.loader.paths))
	for i, path := range g.loader.paths {
		initial[i] = expander.DiscoveredPath{Path: path, Producer: fmt.Sprintf("path %s", path)}
	}
	return initial
}

func (g fluxRenderGraph) renderPath(path expander.DiscoveredPath) error {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = g.loader.root
	}
	full := filepath.Join(baseDir, path.Path)
	if !g.loader.fs.Exists(full) {
		if g.userPath(path.Path) {
			return fmt.Errorf("path %q does not exist", path.Path)
		}
		g.loader.log.V(1).Info("skipping non-existent path", "path", path.Path)
		return nil
	}

	g.loader.log.V(1).Info("rendering path", "path", path.Path, "baseDir", path.BaseDir)
	count := g.loader.render.Size()
	producer := path.Producer
	if producer == "" {
		producer = fmt.Sprintf("path %s", path.Path)
	}
	if g.loader.preview.recursive {
		if err := g.loader.render.AddPathsWithProducer(g.loader.fs, full, producer); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	} else {
		if err := g.loader.render.AddPathWithProducer(g.loader.fs, full, producer); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	}
	if err := g.loader.render.ApplySubstitutionsToNew(count, producer, path.Substitutions); err != nil {
		return fmt.Errorf("failed to apply postBuild substitutions for path %s: %w", full, err)
	}
	if path.Namespace != "" {
		g.loader.render.ApplyNamespaceToNew(count, path.Namespace)
	}
	g.loader.render.MarkProvenanceToNew(count, producer)
	return nil
}

func (g fluxRenderGraph) pathKey(path expander.DiscoveredPath) string {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = g.loader.root
	}
	return filepath.Join(baseDir, path.Path)
}

func (g fluxRenderGraph) userPath(path string) bool {
	for _, userPath := range g.loader.paths {
		if userPath == path {
			return true
		}
	}
	return false
}

func (g fluxRenderGraph) applyFilters() error {
	if g.loader.preview.filters == nil {
		return nil
	}
	for _, filter := range g.loader.preview.filters.Filters {
		if err := g.loader.render.ApplyFilter(filter.Filter); err != nil {
			return err
		}
	}
	return nil
}

func (g fluxRenderGraph) decryptSOPS() error {
	if !g.loader.preview.sopsDecrypt {
		return nil
	}
	if err := sops.DecryptResources(g.loader.render); err != nil {
		return g.loader.clusterErrorf("sops decryption failed: %w", err)
	}
	return nil
}
