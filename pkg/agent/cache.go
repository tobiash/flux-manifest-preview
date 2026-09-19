package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/preview"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// Only serialized bytes and fixed-size lifecycle metadata survive an operation.
// Decoded maps, YAML trees, and render machinery are never held by the cache.
type cachedEntry struct {
	created time.Time
	data    []byte
}

type artifact struct {
	Snapshot bool
	Clusters []string
	Records  []record
	Policies *config.PolicyConfig
}

func (c *cachedEntry) decode(id string, restoreSnapshot bool) (*entry, error) {
	var a artifact
	decoder := json.NewDecoder(bytes.NewReader(c.data))
	decoder.UseNumber()
	if err := decoder.Decode(&a); err != nil {
		return nil, err
	}
	e := &entry{id: id, records: a.Records, policies: a.Policies}
	if !a.Snapshot {
		e.changes = &diff.DiffResult{}
		for _, r := range a.Records {
			summary := r.Summary
			change := diff.ResourceChange{ID: recordID(summary), Cluster: summary.Cluster, Kind: summary.Kind, Name: summary.Name, Namespace: summary.Namespace, Action: summary.Action, Producer: summary.Producer, BeforeOrigin: provenance(summary.BeforeOrigin), AfterOrigin: provenance(summary.AfterOrigin), Old: r.Old, New: r.New}
			p := change.AfterOrigin
			if p == nil {
				p = change.BeforeOrigin
			}
			if p != nil {
				change.Provenance = *p
			}
			switch summary.Action {
			case "added":
				e.changes.Added = append(e.changes.Added, change)
			case "modified":
				e.changes.Modified = append(e.changes.Modified, change)
			case "deleted":
				e.changes.Deleted = append(e.changes.Deleted, change)
			default:
				return nil, errInput
			}
		}
		return e, nil
	}
	e.snapshot = &preview.Snapshot{Complete: true}
	if !restoreSnapshot {
		return e, nil
	}
	e.snapshot.Clusters = make(map[string]*render.Render, len(a.Clusters))
	for _, cluster := range a.Clusters {
		e.snapshot.Clusters[cluster] = render.NewDefaultRender(logr.Discard())
	}
	factory := resmap.NewFactory(resource.NewFactory(nil))
	for _, record := range a.Records {
		// Reparse the original rendered YAML, not JSON maps: scalar tags,
		// quoting, ordering, and comments can affect the existing comparator.
		resources, err := factory.NewResMapFromBytes([]byte(record.YAML))
		if err != nil {
			return nil, err
		}
		r := e.snapshot.Clusters[record.Summary.Cluster]
		if r == nil || resources.Size() != 1 {
			return nil, errInput
		}
		if err := r.Append(resources.Resources()[0]); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func recordID(s ResourceSummary) resid.ResId {
	group, version, ok := strings.Cut(s.APIVersion, "/")
	if !ok {
		version, group = group, ""
	}
	return resid.ResId{Gvk: resid.NewGvk(group, version, s.Kind), Name: s.Name, Namespace: s.Namespace}
}

func provenance(o *Origin) *render.Provenance {
	if o == nil {
		return nil
	}
	return &render.Provenance{Kind: o.Kind, Name: o.Name, Namespace: o.Namespace, Path: o.Path, Text: o.Text}
}

// Render's provenance map has no public setter. Restore the exact saved origins
// on comparison results, rather than inferring them from labels or annotations.
func restoreOrigins(result *diff.DiffResult, before, after []record) {
	type key struct {
		cluster string
		id      resid.ResId
	}
	left, right := make(map[key]*Origin), make(map[key]*Origin)
	for _, r := range before {
		left[key{r.Summary.Cluster, recordID(r.Summary)}] = r.Summary.AfterOrigin
	}
	for _, r := range after {
		right[key{r.Summary.Cluster, recordID(r.Summary)}] = r.Summary.AfterOrigin
	}
	for _, changes := range [][]diff.ResourceChange{result.Added, result.Modified, result.Deleted} {
		for i := range changes {
			c := &changes[i]
			k := key{c.Cluster, c.ID}
			c.BeforeOrigin, c.AfterOrigin = provenance(left[k]), provenance(right[k])
			p := c.AfterOrigin
			if p == nil {
				p = c.BeforeOrigin
			}
			if p != nil {
				c.Provenance, c.Producer = *p, p.String()
			}
		}
	}
}
