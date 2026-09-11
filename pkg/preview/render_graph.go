package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"github.com/tobiash/flux-manifest-preview/pkg/sops"
)

type fluxRenderGraph struct {
	loader repoLoader
}

func (g fluxRenderGraph) Load(ctx context.Context) (*loadRepoResult, error) {
	initial := g.initialPaths()
	roots := make(map[string]bool, len(initial))
	for _, path := range initial {
		roots[g.pathKey(path)] = true
	}
	bootstrap := make(map[string]string)
	visited := make(map[string]bool)
	discovery := expander.DiscoveryRunner{
		Expanders: g.loader.preview.expandersForSource(g.loader.root),
		PathKey:   g.pathKey,
		RenderPath: func(path expander.DiscoveredPath) error {
			return g.renderPathWithBuildFilter(path, func(build expander.DiscoveredPath) bool {
				identity := g.pathKey(build)
				if visited[identity] {
					return false
				}
				visited[identity] = true
				buildContext := build
				buildContext.Producer = ""
				key := g.pathKey(buildContext)
				if producer, ok := bootstrap[key]; ok && producer != build.Producer {
					// Only the first equivalent Flux owner reuses a bootstrap build.
					delete(bootstrap, key)
					return false
				}
				if roots[g.pathKey(path)] {
					bootstrap[key] = build.Producer
				}
				return true
			})
		},
	}
	expanded, err := discovery.Run(ctx, g.loader.render, initial)
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
		path = filepath.Clean(path)
		initial[i] = expander.DiscoveredPath{Path: path, Producer: fmt.Sprintf("path %s", path)}
	}
	return initial
}

func (g fluxRenderGraph) renderPath(path expander.DiscoveredPath) error {
	return g.renderPathWithBuildFilter(path, nil)
}

func (g fluxRenderGraph) renderPathWithBuildFilter(path expander.DiscoveredPath, shouldBuild func(expander.DiscoveredPath) bool) error {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = g.loader.root
	}
	full := filepath.Join(baseDir, path.Path)
	if !g.loader.fs.Exists(full) {
		if g.userPath(path.Path) {
			return fmt.Errorf("path %q does not exist", path.Path)
		}
		return fmt.Errorf("%s: discovered path %q does not exist", path.Producer, full)
	}

	g.loader.log.V(1).Info("rendering path", "path", path.Path, "baseDir", path.BaseDir)
	// Transform each build before merging; raw identities can overlap across contexts.
	built := render.NewDefaultRender(g.loader.log)
	producer := path.Producer
	if producer == "" {
		producer = fmt.Sprintf("path %s", path.Path)
	}
	visit := func(dir string) error {
		build := path
		build.BaseDir = dir
		build.Path = "."
		if shouldBuild != nil && !shouldBuild(build) {
			return nil
		}
		return built.AddPathWithProducer(g.loader.fs, dir, producer)
	}
	if g.loader.preview.recursive {
		if err := render.WalkPaths(g.loader.fs, full, visit); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	} else {
		if err := visit(full); err != nil {
			return fmt.Errorf("failed to add path %s: %w", full, err)
		}
	}
	if err := built.ApplySubstitutionsToNew(0, producer, path.Substitutions); err != nil {
		return fmt.Errorf("failed to apply postBuild substitutions for path %s: %w", full, err)
	}
	if path.Namespace != "" {
		if err := built.ApplyNamespaceToNew(0, path.Namespace); err != nil {
			return fmt.Errorf("applying target namespace for %s: %w", producer, err)
		}
	}
	built.MarkProvenanceToNew(0, producer)
	return g.loader.render.AbsorbAll(built)
}

func (g fluxRenderGraph) pathKey(path expander.DiscoveredPath) string {
	baseDir := path.BaseDir
	if baseDir == "" {
		baseDir = g.loader.root
	}
	path.Path = filepath.Join(baseDir, path.Path)
	path.BaseDir = ""
	if len(path.Substitutions) == 0 {
		path.Substitutions = nil
	}
	key, _ := json.Marshal(path) // DiscoveredPath contains only JSON-safe strings and maps.
	return string(key)
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
