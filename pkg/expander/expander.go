package expander

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/api/resmap"
)

// ExpandResult holds the output of an expander.
type ExpandResult struct {
	// Resources contains additional resources produced by the expander.
	Resources resmap.ResMap
	// DiscoveredPaths contains new paths that should be rendered in the
	// next iteration of the expansion loop (e.g. Flux Kustomization spec.path).
	DiscoveredPaths []string
}

// Expander is the interface that each resource expander must implement.
// An expander takes a rendered set of Kubernetes resources and produces
// additional resources and/or discovers new paths to render.
type Expander interface {
	// Expand processes the given render and returns expansion results.
	Expand(ctx context.Context, r *render.Render) (*ExpandResult, error)
}

// Registry holds a collection of expanders and runs them in order.
type Registry struct {
	expanders []Expander
	log       logr.Logger
}

// NewRegistry creates a new empty expander registry.
func NewRegistry(log logr.Logger) *Registry {
	return &Registry{
		log: log,
	}
}

// Register adds an expander to the registry.
func (r *Registry) Register(e Expander) {
	r.expanders = append(r.expanders, e)
}

// Expand runs all registered expanders on the given render and accumulates results.
func (r *Registry) Expand(ctx context.Context, render *render.Render) (*ExpandResult, error) {
	result := &ExpandResult{Resources: resmap.New()}
	for i, e := range r.expanders {
		r.log.V(1).Info("running expander", "index", i)
		expanded, err := e.Expand(ctx, render)
		if err != nil {
			return nil, fmt.Errorf("expander %d failed: %w", i, err)
		}
		if expanded.Resources != nil {
			if err := result.Resources.AbsorbAll(expanded.Resources); err != nil {
				return nil, fmt.Errorf("expander %d absorb failed: %w", i, err)
			}
		}
		result.DiscoveredPaths = append(result.DiscoveredPaths, expanded.DiscoveredPaths...)
	}
	return result, nil
}
