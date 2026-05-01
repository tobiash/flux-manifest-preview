package diff

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	k8qdiff "github.com/tobiash/k8q/pkg/diff"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// ObjectRef mirrors the Kubernetes ObjectReference shape.
type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// DiffResultJSON is the JSON representation of a manifest diff.
type DiffResultJSON struct {
	Added    []map[string]any `json:"added"`
	Deleted  []ObjectRef      `json:"deleted"`
	Modified []DiffChangeJSON `json:"modified"`
}

// DiffChangeJSON represents a modified resource with before/after snapshots.
type DiffChangeJSON struct {
	ObjectRef   ObjectRef      `json:"objectRef"`
	Cluster     string         `json:"cluster,omitempty"`
	Producer    string         `json:"producer,omitempty"`
	Old         map[string]any `json:"old"`
	New         map[string]any `json:"new"`
	UnifiedDiff string         `json:"unifiedDiff"`
}

// ResourceChange describes a single resource change in a diff.
type ResourceChange struct {
	Cluster   string
	ID        resid.ResId
	Kind      string
	Name      string
	Namespace string
	Producer  string
	Action    string // added, deleted, modified
	Old       map[string]any
	New       map[string]any
}

// DiffResult holds the structured result of a diff between two renders.
type DiffResult struct {
	Added    []ResourceChange
	Deleted  []ResourceChange
	Modified []ResourceChange
}

// TotalChanged returns the total number of changed resources.
func (r *DiffResult) TotalChanged() int {
	return len(r.Added) + len(r.Deleted) + len(r.Modified)
}

// ByKind returns counts grouped by resource Kind.
func (r *DiffResult) ByKind() map[string]int {
	m := make(map[string]int)
	for _, c := range r.Added {
		m[c.Kind]++
	}
	for _, c := range r.Deleted {
		m[c.Kind]++
	}
	for _, c := range r.Modified {
		m[c.Kind]++
	}
	return m
}

// ByCluster returns counts grouped by cluster name.
func (r *DiffResult) ByCluster() map[string]int {
	m := make(map[string]int)
	for _, c := range r.Added {
		m[c.Cluster]++
	}
	for _, c := range r.Deleted {
		m[c.Cluster]++
	}
	for _, c := range r.Modified {
		m[c.Cluster]++
	}
	return m
}

// ToJSON converts the diff result to a JSON-serializable structure.
func (r *DiffResult) ToJSON() *DiffResultJSON {
	out := &DiffResultJSON{}
	for _, c := range r.Added {
		out.Added = append(out.Added, c.New)
	}
	for _, c := range r.Deleted {
		out.Deleted = append(out.Deleted, ObjectRef{
			APIVersion: gvkAPIVersion(c.ID.Group, c.ID.Version),
			Kind:       c.Kind,
			Name:       c.Name,
			Namespace:  c.Namespace,
		})
	}
	for _, c := range r.Modified {
		var diffBuf bytes.Buffer
		oldYaml := mustYamlMap(c.Old)
		newYaml := mustYamlMap(c.New)
		u := computeDiff(c.ID.String(), oldYaml, newYaml)
		formatUnified(&diffBuf, u)
		out.Modified = append(out.Modified, DiffChangeJSON{
			ObjectRef: ObjectRef{
				APIVersion: gvkAPIVersion(c.ID.Group, c.ID.Version),
				Kind:       c.Kind,
				Name:       c.Name,
				Namespace:  c.Namespace,
			},
			Cluster:     c.Cluster,
			Producer:    c.Producer,
			Old:         c.Old,
			New:         c.New,
			UnifiedDiff: diffBuf.String(),
		})
	}
	return out
}

// gvkAPIVersion returns an apiVersion string from a group and version.
// If group is empty, it returns the version alone (for core resources).
func gvkAPIVersion(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}

func mustYamlMap(m map[string]any) string {
	if m == nil {
		return ""
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

// DiffWithResult computes a unified diff and returns structured change metadata.
// The unified diff text is written to w.
func DiffWithResult(a, b *render.Render, w io.Writer) (*DiffResult, error) {
	result, err := k8qdiff.DiffNodes(renderToRNodes(a), renderToRNodes(b))
	if err != nil {
		return nil, err
	}

	fmpResult := &DiffResult{}

	for _, key := range result.Added {
		id := objectRefToResId(key)
		r, _ := b.GetByCurrentId(id)
		if r == nil {
			continue
		}
		yaml := r.MustYaml()
		obj, _ := r.Map()
		fmpResult.Added = append(fmpResult.Added, ResourceChange{
			ID:        id,
			Kind:      r.GetKind(),
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
			Producer:  b.ProducerForID(id),
			Action:    "added",
			New:       obj,
		})
		u := computeDiff(id.String(), "", yaml)
		formatUnified(w, u)
	}

	for _, key := range result.Removed {
		id := objectRefToResId(key)
		r, _ := a.GetByCurrentId(id)
		if r == nil {
			continue
		}
		yaml := r.MustYaml()
		obj, _ := r.Map()
		fmpResult.Deleted = append(fmpResult.Deleted, ResourceChange{
			ID:        id,
			Kind:      r.GetKind(),
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
			Producer:  a.ProducerForID(id),
			Action:    "deleted",
			Old:       obj,
		})
		u := computeDiff(id.String(), yaml, "")
		formatUnified(w, u)
	}

	for _, change := range result.Modified {
		id := objectRefToResId(change.Key)
		ar, _ := a.GetByCurrentId(id)
		br, _ := b.GetByCurrentId(id)

		if ar == nil || br == nil {
			continue
		}

		aYaml := ar.MustYaml()
		bYaml := br.MustYaml()
		if aYaml == bYaml {
			continue
		}

		fmpResult.Modified = append(fmpResult.Modified, ResourceChange{
			ID:        id,
			Kind:      br.GetKind(),
			Name:      br.GetName(),
			Namespace: br.GetNamespace(),
			Producer:  b.ProducerForID(id),
			Action:    "modified",
			Old:       mapOrNil(ar),
			New:       mapOrNil(br),
		})
		u := computeDiff(id.String(), aYaml, bYaml)
		formatUnified(w, u)
	}

	return fmpResult, nil
}

// DiffWithResultClustered diffs two sets of renders keyed by cluster name.
// Clusters present on both sides are diffed normally. Clusters only on the
// left side produce all-deleted results; clusters only on the right produce
// all-added results.
func DiffWithResultClustered(left, right map[string]*render.Render, w io.Writer) (*DiffResult, error) {
	fmpResult := &DiffResult{}

	allClusters := make(map[string]bool)
	for c := range left {
		allClusters[c] = true
	}
	for c := range right {
		allClusters[c] = true
	}

	for cluster := range allClusters {
		lr, lok := left[cluster]
		rr, rok := right[cluster]

		if !lok {
			// Cluster only on right → all added
			result, err := DiffWithResult(render.NewDefaultRender(logr.Discard()), rr, w)
			if err != nil {
				return nil, err
			}
			for i := range result.Added {
				result.Added[i].Cluster = cluster
			}
			fmpResult.Added = append(fmpResult.Added, result.Added...)
			continue
		}

		if !rok {
			// Cluster only on left → all deleted
			result, err := DiffWithResult(lr, render.NewDefaultRender(logr.Discard()), w)
			if err != nil {
				return nil, err
			}
			for i := range result.Deleted {
				result.Deleted[i].Cluster = cluster
			}
			fmpResult.Deleted = append(fmpResult.Deleted, result.Deleted...)
			continue
		}

		// Cluster on both sides → normal diff
		result, err := DiffWithResult(lr, rr, w)
		if err != nil {
			return nil, err
		}
		for i := range result.Added {
			result.Added[i].Cluster = cluster
		}
		for i := range result.Deleted {
			result.Deleted[i].Cluster = cluster
		}
		for i := range result.Modified {
			result.Modified[i].Cluster = cluster
		}
		fmpResult.Added = append(fmpResult.Added, result.Added...)
		fmpResult.Deleted = append(fmpResult.Deleted, result.Deleted...)
		fmpResult.Modified = append(fmpResult.Modified, result.Modified...)
	}

	return fmpResult, nil
}

func mapOrNil(res interface {
	Map() (map[string]any, error)
}) map[string]any {
	if res == nil {
		return nil
	}
	obj, err := res.Map()
	if err != nil {
		return nil
	}
	return obj
}
