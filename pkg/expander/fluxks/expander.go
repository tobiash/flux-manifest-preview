package fluxks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	fluxksv1 "github.com/fluxcd/kustomize-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"k8s.io/apimachinery/pkg/runtime"
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

		ks, err := decodeKustomization(res)
		if err != nil {
			e.log.Error(err, "skipping Flux Kustomization", "name", res.GetName())
			continue
		}
		if ks.Spec.Path == "" {
			e.log.Error(fmt.Errorf("Kustomization %s/%s has no spec.path", ks.Namespace, ks.Name), "skipping Flux Kustomization", "name", ks.Name)
			continue
		}

		path := ks.Spec.Path
		path = strings.TrimPrefix(path, "./")
		path = filepath.Clean(path)
		if path == "." {
			continue
		}

		dp := expander.DiscoveredPath{
			Path:     path,
			Producer: fmt.Sprintf("Kustomization %s/%s", ks.Namespace, ks.Name),
		}

		if ks.Spec.TargetNamespace != "" {
			dp.Namespace = ks.Spec.TargetNamespace
		}

		if e.resolver != nil && ks.Spec.SourceRef.Kind == "GitRepository" {
			ns := ks.Spec.SourceRef.Namespace
			if ns == "" {
				ns = ks.Namespace
			}
			if baseDir, ok := e.resolver.ResolvePath(ns, ks.Spec.SourceRef.Name); ok {
				dp.BaseDir = baseDir
			}
		}

		e.log.V(1).Info("discovered Flux Kustomization path",
			"path", dp.Path, "baseDir", dp.BaseDir, "name", ks.Name, "namespace", ks.Namespace)
		paths = append(paths, dp)
	}

	return &expander.ExpandResult{DiscoveredPaths: paths}, nil
}

func decodeKustomization(res *resource.Resource) (*fluxksv1.Kustomization, error) {
	var ks fluxksv1.Kustomization
	m, err := res.Map()
	if err != nil {
		return nil, fmt.Errorf("reading resource map: %w", err)
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &ks); err != nil {
		return nil, fmt.Errorf("decoding Kustomization %s/%s: %w", res.GetNamespace(), res.GetName(), err)
	}
	return &ks, nil
}
