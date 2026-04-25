package diff

import (
	"fmt"
	"io"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	k8qdiff "github.com/tobiash/k8q/pkg/diff"
	"sigs.k8s.io/kustomize/kyaml/resid"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

// renderToRNodes converts a render.Render to a slice of *yaml.RNode for
// consumption by the k8q diff engine. Nodes are deep-copied because the
// diff engine applies a ReorderFilter in-place.
func renderToRNodes(r *render.Render) []*yaml.RNode {
	resources := r.Resources()
	nodes := make([]*yaml.RNode, len(resources))
	for i, res := range resources {
		nodes[i] = res.Copy()
	}
	return nodes
}

// objectRefToResId converts a k8q ObjectRef to a kustomize resid.ResId.
func objectRefToResId(ref k8qdiff.ObjectRef) resid.ResId {
	return resid.NewResIdWithNamespace(
		resid.Gvk{
			Group:   gvkGroup(ref.APIVersion),
			Version: gvkVersion(ref.APIVersion),
			Kind:    ref.Kind,
		},
		ref.Name,
		ref.Namespace,
	)
}

func gvkGroup(apiVersion string) string {
	if i := len(apiVersion) - 1; i >= 0 {
		for j := i; j >= 0; j-- {
			if apiVersion[j] == '/' {
				return apiVersion[:j]
			}
		}
	}
	return ""
}

func gvkVersion(apiVersion string) string {
	for i := len(apiVersion) - 1; i >= 0; i-- {
		if apiVersion[i] == '/' {
			return apiVersion[i+1:]
		}
	}
	return apiVersion
}

func computeDiff(name, before, after string) gotextdiff.Unified {
	edits := myers.ComputeEdits(span.URIFromPath(name), before, after)
	return gotextdiff.ToUnified(name, name, before, edits)
}

func formatUnified(w io.Writer, u gotextdiff.Unified) {
	fmt.Fprintf(w, "%v", u)
}

// Diff computes a unified diff between two Renders and writes the result to w.
func Diff(a, b *render.Render, w io.Writer) error {
	result, err := k8qdiff.DiffNodes(renderToRNodes(a), renderToRNodes(b))
	if err != nil {
		return err
	}

	for _, key := range result.Removed {
		id := objectRefToResId(key)
		r, _ := a.GetByCurrentId(id)
		if r == nil {
			continue
		}
		yaml := r.MustYaml()
		formatUnified(w, computeDiff(id.String(), yaml, ""))
	}

	for _, key := range result.Added {
		id := objectRefToResId(key)
		r, _ := b.GetByCurrentId(id)
		if r == nil {
			continue
		}
		yaml := r.MustYaml()
		formatUnified(w, computeDiff(id.String(), "", yaml))
	}

	for _, change := range result.Modified {
		formatUnified(w, computeDiff(change.Key.String(), change.Before, change.After))
	}

	return nil
}
