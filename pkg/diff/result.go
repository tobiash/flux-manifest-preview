package diff

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"

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
	oldYAML   string
	newYAML   string
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

// ChangeBreakdown holds added/modified/deleted counts for a category like kind.
type ChangeBreakdown struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
	Total    int `json:"total"`
}

// BuildBreakdown groups changes by the given key function and returns breakdown counts.
func BuildBreakdown(result *DiffResult, keyFn func(ResourceChange) string) map[string]ChangeBreakdown {
	breakdown := make(map[string]ChangeBreakdown)
	for _, change := range result.Added {
		key := keyFn(change)
		entry := breakdown[key]
		entry.Added++
		entry.Total++
		breakdown[key] = entry
	}
	for _, change := range result.Modified {
		key := keyFn(change)
		entry := breakdown[key]
		entry.Modified++
		entry.Total++
		breakdown[key] = entry
	}
	for _, change := range result.Deleted {
		key := keyFn(change)
		entry := breakdown[key]
		entry.Deleted++
		entry.Total++
		breakdown[key] = entry
	}
	return breakdown
}

// KindBreakdown returns change counts grouped by resource Kind.
func KindBreakdown(result *DiffResult) map[string]ChangeBreakdown {
	return BuildBreakdown(result, func(c ResourceChange) string { return c.Kind })
}

// ClusterBreakdown returns change counts grouped by cluster name.
func ClusterBreakdown(result *DiffResult) map[string]ChangeBreakdown {
	return BuildBreakdown(result, func(c ResourceChange) string { return c.Cluster })
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
		oldYAML := mustYAMLMap(c.Old)
		newYAML := mustYAMLMap(c.New)
		u := computeDiff(c.ID.String(), oldYAML, newYAML)
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

func mustYAMLMap(m map[string]any) string {
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
	result, err := ChangeSet(a, b)
	if err != nil {
		return nil, err
	}
	result.WriteUnified(w)
	return result, nil
}

// ChangeSet computes structured resource changes between two renders.
func ChangeSet(a, b *render.Render) (*DiffResult, error) {
	result, err := k8qdiff.DiffNodes(renderToRNodes(a), renderToRNodes(b))
	if err != nil {
		return nil, err
	}

	fmpResult := &DiffResult{}

	for _, key := range result.Added {
		id := objectRefToResID(key)
		view, ok := b.ResourceViewForID(id)
		if !ok {
			continue
		}
		fmpResult.Added = append(fmpResult.Added, ResourceChange{
			ID:        id,
			Kind:      view.Kind,
			Name:      view.Name,
			Namespace: view.Namespace,
			Producer:  view.Producer,
			Action:    "added",
			New:       view.Object,
			newYAML:   view.YAML,
		})
	}

	for _, key := range result.Removed {
		id := objectRefToResID(key)
		view, ok := a.ResourceViewForID(id)
		if !ok {
			continue
		}
		fmpResult.Deleted = append(fmpResult.Deleted, ResourceChange{
			ID:        id,
			Kind:      view.Kind,
			Name:      view.Name,
			Namespace: view.Namespace,
			Producer:  view.Producer,
			Action:    "deleted",
			Old:       view.Object,
			oldYAML:   view.YAML,
		})
	}

	for _, change := range result.Modified {
		id := objectRefToResID(change.Key)
		before, ok := a.ResourceViewForID(id)
		if !ok {
			continue
		}
		after, ok := b.ResourceViewForID(id)
		if !ok {
			continue
		}

		aYAML := before.YAML
		bYAML := after.YAML
		if aYAML == bYAML {
			continue
		}

		fmpResult.Modified = append(fmpResult.Modified, ResourceChange{
			ID:        id,
			Kind:      after.Kind,
			Name:      after.Name,
			Namespace: after.Namespace,
			Producer:  after.Producer,
			Action:    "modified",
			Old:       before.Object,
			New:       after.Object,
			oldYAML:   aYAML,
			newYAML:   bYAML,
		})
	}

	fmpResult.Sort()
	return fmpResult, nil
}

// WriteUnified writes the unified text representation of a change set.
func (r *DiffResult) WriteUnified(w io.Writer) {
	for _, change := range r.Deleted {
		formatUnified(w, computeDiff(change.ID.String(), change.oldText(), ""))
	}
	for _, change := range r.Added {
		formatUnified(w, computeDiff(change.ID.String(), "", change.newText()))
	}
	for _, change := range r.Modified {
		formatUnified(w, computeDiff(change.ID.String(), change.oldText(), change.newText()))
	}
}

func (c ResourceChange) oldText() string {
	if c.oldYAML != "" {
		return c.oldYAML
	}
	return mustYAMLMap(c.Old)
}

func (c ResourceChange) newText() string {
	if c.newYAML != "" {
		return c.newYAML
	}
	return mustYAMLMap(c.New)
}

// Sort applies deterministic ordering to all change groups.
func (r *DiffResult) Sort() {
	sortChanges(r.Added)
	sortChanges(r.Deleted)
	sortChanges(r.Modified)
}

func sortChanges(changes []ResourceChange) {
	sort.Slice(changes, func(i, j int) bool {
		return changeSortKey(changes[i]) < changeSortKey(changes[j])
	})
}

func changeSortKey(change ResourceChange) string {
	return change.Cluster + "\x00" + change.Kind + "\x00" + change.Namespace + "\x00" + change.Name + "\x00" + change.Action
}

// DiffWithResultClustered diffs two sets of renders keyed by cluster name.
// Clusters present on both sides are diffed normally. Clusters only on the
// left side produce all-deleted results; clusters only on the right produce
// all-added results.
func DiffWithResultClustered(left, right map[string]*render.Render, w io.Writer) (*DiffResult, error) {
	fmpResult := &DiffResult{}

	allClusters := make(map[string]struct{})
	for c := range left {
		allClusters[c] = struct{}{}
	}
	for c := range right {
		allClusters[c] = struct{}{}
	}

	clusters := make([]string, 0, len(allClusters))
	for cluster := range allClusters {
		clusters = append(clusters, cluster)
	}
	sort.Strings(clusters)

	for _, cluster := range clusters {
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

	fmpResult.Sort()
	return fmpResult, nil
}
