package expander

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/tobiash/flux-manifest-preview/pkg/render"
)

// PathKeyFunc returns the stable identity used to deduplicate discovered paths.
type PathKeyFunc func(DiscoveredPath) string

// RenderPathFunc renders one discovered path into the current resource set.
type RenderPathFunc func(DiscoveredPath) error

// DiscoveryRunner repeatedly renders discovered paths and expands the resulting
// resource set until Flux discovery reaches a fixed point.
type DiscoveryRunner struct {
	Expanders     *Registry
	PathKey       PathKeyFunc
	RenderPath    RenderPathFunc
	MaxIterations int
}

// Run renders initial and discovered paths, accumulating non-fatal expansion errors.
func (r DiscoveryRunner) Run(ctx context.Context, resources *render.Render, initial []DiscoveredPath) (*ExpandResult, error) {
	if r.RenderPath == nil {
		return nil, fmt.Errorf("render path function is required")
	}
	maxIterations := r.MaxIterations
	if maxIterations == 0 {
		maxIterations = 100
	}

	queue := append([]DiscoveredPath(nil), initial...)
	visited := make(map[string]struct{})
	result := &ExpandResult{}
	var deferredErrors []error

	for iteration := 0; len(queue) > 0; iteration++ {
		if iteration > maxIterations {
			return nil, fmt.Errorf("expansion loop exceeded %d iterations, possible cycle", maxIterations)
		}

		for _, path := range queue {
			key := r.pathKey(path)
			if _, ok := visited[key]; ok {
				continue
			}
			visited[key] = struct{}{}

			if err := r.RenderPath(path); err != nil {
				return nil, err
			}
		}

		queue = nil
		if r.Expanders == nil {
			continue
		}

		expanded, err := r.Expanders.Expand(ctx, resources)
		if err != nil {
			return nil, fmt.Errorf("failed to expand: %w", err)
		}
		result.Errors = append(result.Errors, expanded.Errors...)
		deferredErrors = expanded.DeferredErrors
		if expanded.Resources != nil {
			if err := resources.AbsorbAll(expanded.Resources); err != nil {
				return nil, fmt.Errorf("failed to absorb expanded resources: %w", err)
			}
		}
		for _, path := range expanded.DiscoveredPaths {
			if _, ok := visited[r.pathKey(path)]; !ok {
				queue = append(queue, path)
			}
		}
	}

	result.Errors = append(result.Errors, deferredErrors...)
	return result, nil
}

func (r DiscoveryRunner) pathKey(path DiscoveredPath) string {
	if r.PathKey != nil {
		return r.PathKey(path)
	}
	path.Path = filepath.Join(path.BaseDir, path.Path)
	path.BaseDir = ""
	if len(path.Substitutions) == 0 {
		path.Substitutions = nil
	}
	key, _ := json.Marshal(path) // DiscoveredPath contains only JSON-safe strings and maps.
	return string(key)
}
