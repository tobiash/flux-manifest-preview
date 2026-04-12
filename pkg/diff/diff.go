package diff

import (
	"fmt"
	"io"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// Diff computes a unified diff between two Renders and writes the result to w.

func Diff(a, b *render.Render, w io.Writer) error {
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

	for _, c := range added {
		r, _ := b.GetByCurrentId(c)
		yaml := r.MustYaml()
		edits := myers.ComputeEdits(span.URIFromPath(c.String()), "", yaml)
		fmt.Fprint(w, gotextdiff.ToUnified(c.String(), c.String(), "", edits))
	}

	for _, d := range deleted {
		r, _ := a.GetByCurrentId(d)
		yaml := r.MustYaml()
		edits := myers.ComputeEdits(span.URIFromPath(d.String()), yaml, "")
		fmt.Fprint(w, gotextdiff.ToUnified(d.String(), d.String(), yaml, edits))
	}

	for _, m := range modified {
		ar, _ := a.GetByCurrentId(m)
		br, _ := b.GetByCurrentId(m)

		aYaml := ar.MustYaml()
		bYaml := br.MustYaml()
		if aYaml == bYaml {
			continue
		}

		edits := myers.ComputeEdits(span.URIFromPath(m.String()), aYaml, bYaml)
		fmt.Fprint(w, gotextdiff.ToUnified(m.String(), m.String(), aYaml, edits))
	}
	return nil
}
