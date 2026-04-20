package expander

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/api/resmap"
)

type DiscoveredPath struct {
	Path      string
	BaseDir   string
	Namespace string
}

type ExpandResult struct {
	Resources       resmap.ResMap
	DiscoveredPaths []DiscoveredPath
}

type Expander interface {
	Expand(ctx context.Context, r *render.Render) (*ExpandResult, error)
}

type Registry struct {
	expanders []Expander
	log       logr.Logger
}

func NewRegistry(log logr.Logger) *Registry {
	return &Registry{
		log: log,
	}
}

func (r *Registry) Register(e Expander) {
	r.expanders = append(r.expanders, e)
}

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
