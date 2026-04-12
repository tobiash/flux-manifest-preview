package fluxks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

var fluxKSGVK = resid.NewGvk("kustomize.toolkit.fluxcd.io", "v1", "Kustomization")

// matchGVK reports true if the resource's GVK matches the target by group and kind.
func matchGVK(resGvk resid.Gvk, target resid.Gvk) bool {
	return resGvk.Group == target.Group && resGvk.Kind == target.Kind
}

// Expander discovers Flux Kustomization CRs in the resource set and returns
// their spec.path values as DiscoveredPaths for the next iteration of the
// expansion loop. It does not produce additional resources.
type Expander struct {
	log logr.Logger
}

// NewExpander creates a Flux Kustomization expander.
func NewExpander(log logr.Logger) *Expander {
	return &Expander{log: log}
}

func (e *Expander) Expand(_ context.Context, r *render.Render) (*expander.ExpandResult, error) {
	var paths []string

	for _, res := range r.Resources() {
		gvk := res.GetGvk()
		if !matchGVK(gvk, fluxKSGVK) {
			continue
		}

		path, err := extractPath(res)
		if err != nil {
			e.log.Error(err, "skipping Flux Kustomization", "name", res.GetName())
			continue
		}

		// Clean the path: strip leading ./ and ensure it's relative
		path = strings.TrimPrefix(path, "./")
		path = filepath.Clean(path)
		if path == "." {
			continue
		}

		e.log.Info("discovered Flux Kustomization path",
			"path", path, "name", res.GetName(), "namespace", res.GetNamespace())
		paths = append(paths, path)
	}

	return &expander.ExpandResult{DiscoveredPaths: paths}, nil
}

// extractPath reads spec.path from a Flux Kustomization resource.
func extractPath(res *resource.Resource) (string, error) {
	m, err := res.Map()
	if err != nil {
		return "", fmt.Errorf("reading resource map: %w", err)
	}
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Kustomization %s/%s has no spec", res.GetNamespace(), res.GetName())
	}
	path, ok, err := unstructured.NestedString(spec, "path")
	if err != nil {
		return "", fmt.Errorf("reading spec.path: %w", err)
	}
	if !ok || path == "" {
		return "", fmt.Errorf("Kustomization %s/%s has no spec.path", res.GetNamespace(), res.GetName())
	}
	return path, nil
}
