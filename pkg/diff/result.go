package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
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
	Added    []map[string]any  `json:"added"`
	Deleted  []ObjectRef       `json:"deleted"`
	Modified []DiffChangeJSON  `json:"modified"`
}

// DiffChangeJSON represents a modified resource with before/after snapshots.
type DiffChangeJSON struct {
	ObjectRef   ObjectRef      `json:"objectRef"`
	Producer    string         `json:"producer,omitempty"`
	Old         map[string]any `json:"old"`
	New         map[string]any `json:"new"`
	UnifiedDiff string         `json:"unifiedDiff"`
}

// ResourceChange describes a single resource change in a diff.
type ResourceChange struct {
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

// ToJSON converts the diff result to a JSON-serializable structure.
func (r *DiffResult) ToJSON() *DiffResultJSON {
	out := &DiffResultJSON{}
	for _, c := range r.Added {
		out.Added = append(out.Added, c.New)
	}
	for _, c := range r.Deleted {
		out.Deleted = append(out.Deleted, ObjectRef{
			APIVersion: gvkAPIVersion(c.ID.Gvk.Group, c.ID.Gvk.Version),
			Kind:       c.Kind,
			Name:       c.Name,
			Namespace:  c.Namespace,
		})
	}
	for _, c := range r.Modified {
		var diffBuf bytes.Buffer
		oldYaml := mustYamlMap(c.Old)
		newYaml := mustYamlMap(c.New)
		edits := myers.ComputeEdits(span.URIFromPath(c.ID.String()), oldYaml, newYaml)
		if _, err := fmt.Fprint(&diffBuf, gotextdiff.ToUnified(c.ID.String(), c.ID.String(), oldYaml, edits)); err == nil {
			// ignore write error
		}
		out.Modified = append(out.Modified, DiffChangeJSON{
			ObjectRef: ObjectRef{
				APIVersion: gvkAPIVersion(c.ID.Gvk.Group, c.ID.Gvk.Version),
				Kind:       c.Kind,
				Name:       c.Name,
				Namespace:  c.Namespace,
			},
			Producer:    c.Producer,
			Old:         c.Old,
			New:         c.New,
			UnifiedDiff: diffBuf.String(),
		})
	}
	return out
}

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
	var added, deleted, modified []resid.ResId
	for _, ra := range a.Resources() {
		if _, err := b.GetByCurrentId(ra.CurId()); err != nil {
			deleted = append(deleted, ra.CurId())
		} else {
			modified = append(modified, ra.CurId())
		}
	}
	for _, rb := range b.Resources() {
		if _, err := a.GetByCurrentId(rb.CurId()); err != nil {
			added = append(added, rb.CurId())
		}
	}

	result := &DiffResult{}

	for _, c := range added {
		r, _ := b.GetByCurrentId(c)
		yaml := r.MustYaml()
		obj, _ := r.Map()
		result.Added = append(result.Added, ResourceChange{
			ID:        c,
			Kind:      r.GetKind(),
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
			Producer:  b.ProducerForID(c),
			Action:    "added",
			New:       obj,
		})
		edits := myers.ComputeEdits(span.URIFromPath(c.String()), "", yaml)
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(c.String(), c.String(), "", edits)); err != nil {
			return nil, err
		}
	}

	for _, d := range deleted {
		r, _ := a.GetByCurrentId(d)
		yaml := r.MustYaml()
		obj, _ := r.Map()
		result.Deleted = append(result.Deleted, ResourceChange{
			ID:        d,
			Kind:      r.GetKind(),
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
			Producer:  a.ProducerForID(d),
			Action:    "deleted",
			Old:       obj,
		})
		edits := myers.ComputeEdits(span.URIFromPath(d.String()), yaml, "")
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(d.String(), d.String(), yaml, edits)); err != nil {
			return nil, err
		}
	}

	for _, m := range modified {
		ar, _ := a.GetByCurrentId(m)
		br, _ := b.GetByCurrentId(m)

		aYaml := ar.MustYaml()
		bYaml := br.MustYaml()
		if aYaml == bYaml {
			continue
		}

		result.Modified = append(result.Modified, ResourceChange{
			ID:        m,
			Kind:      br.GetKind(),
			Name:      br.GetName(),
			Namespace: br.GetNamespace(),
			Producer:  b.ProducerForID(m),
			Action:    "modified",
			Old:       mapOrNil(ar),
			New:       mapOrNil(br),
		})
		edits := myers.ComputeEdits(span.URIFromPath(m.String()), aYaml, bYaml)
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(m.String(), m.String(), aYaml, edits)); err != nil {
			return nil, err
		}
	}
	return result, nil
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
