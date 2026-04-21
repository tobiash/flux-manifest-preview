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

// SourceResolver resolves GitRepository source references to local paths.
type SourceResolver interface {
	ResolvePath(namespace, name string) (string, bool)
}

// Expander discovers Flux Kustomization CRs in the resource set and returns
// their spec.path values as DiscoveredPaths for the next iteration of the
// expansion loop.
type Expander struct {
	log      logr.Logger
	resolver SourceResolver
}

// NewExpander creates a Flux Kustomization expander.
func NewExpander(log logr.Logger) *Expander {
	return &Expander{log: log}
}

// NewExpanderWithResolver creates a Flux Kustomization expander with GitRepository resolution.
func NewExpanderWithResolver(log logr.Logger, resolver SourceResolver) *Expander {
	return &Expander{log: log, resolver: resolver}
}

func (e *Expander) Expand(_ context.Context, r *render.Render) (*expander.ExpandResult, error) {
	var paths []expander.DiscoveredPath

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

		dp := expander.DiscoveredPath{
			Path:     path,
			Producer: fmt.Sprintf("Kustomization %s/%s", res.GetNamespace(), res.GetName()),
		}

		// Extract targetNamespace from spec.
		if tn := extractTargetNamespace(res); tn != "" {
			dp.Namespace = tn
		}

		// Resolve sourceRef if a resolver is available.
		if e.resolver != nil {
			srcRef := extractSourceRef(res)
			if srcRef.kind == "GitRepository" {
				ns := srcRef.namespace
				if ns == "" {
					ns = res.GetNamespace()
				}
				if baseDir, ok := e.resolver.ResolvePath(ns, srcRef.name); ok {
					dp.BaseDir = baseDir
				}
			}
		}

		e.log.V(1).Info("discovered Flux Kustomization path",
			"path", dp.Path, "baseDir", dp.BaseDir, "name", res.GetName(), "namespace", res.GetNamespace())
		paths = append(paths, dp)
	}

	return &expander.ExpandResult{DiscoveredPaths: paths}, nil
}

type sourceRef struct {
	kind      string
	name      string
	namespace string
}

func extractSourceRef(res *resource.Resource) sourceRef {
	m, err := res.Map()
	if err != nil {
		return sourceRef{}
	}
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return sourceRef{}
	}
	ref, ok := spec["sourceRef"].(map[string]any)
	if !ok {
		return sourceRef{}
	}
	kind, _, _ := unstructured.NestedString(ref, "kind")
	name, _, _ := unstructured.NestedString(ref, "name")
	ns, _, _ := unstructured.NestedString(ref, "namespace")
	return sourceRef{kind: kind, name: name, namespace: ns}
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

// extractTargetNamespace reads spec.targetNamespace from a Flux Kustomization.
func extractTargetNamespace(res *resource.Resource) string {
	m, err := res.Map()
	if err != nil {
		return ""
	}
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return ""
	}
	tn, _, _ := unstructured.NestedString(spec, "targetNamespace")
	return tn
}
