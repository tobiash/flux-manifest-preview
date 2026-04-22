package diff

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// Diff computes a unified diff between two Renders and writes the result to w.
// If exportDir is provided, the rendered manifests from the target (b) will be
// written as individual YAML files to the directory. If exportChangedOnly is true,
// only the added or modified resources will be exported.
func Diff(a, b *render.Render, w io.Writer, exportDir string, exportChangedOnly bool) error {
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

	changedIds := make(map[string]bool)

	for _, c := range added {
		r, _ := b.GetByCurrentId(c)
		yaml := r.MustYaml()
		changedIds[c.String()] = true
		edits := myers.ComputeEdits(span.URIFromPath(c.String()), "", yaml)
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(c.String(), c.String(), "", edits)); err != nil {
			return err
		}
	}

	for _, d := range deleted {
		r, _ := a.GetByCurrentId(d)
		yaml := r.MustYaml()
		edits := myers.ComputeEdits(span.URIFromPath(d.String()), yaml, "")
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(d.String(), d.String(), yaml, edits)); err != nil {
			return err
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

		changedIds[m.String()] = true
		edits := myers.ComputeEdits(span.URIFromPath(m.String()), aYaml, bYaml)
		if _, err := fmt.Fprint(w, gotextdiff.ToUnified(m.String(), m.String(), aYaml, edits)); err != nil {
			return err
		}
	}

	if exportDir != "" {
		if err := os.MkdirAll(exportDir, 0755); err != nil {
			return fmt.Errorf("creating export dir: %w", err)
		}
		for _, rb := range b.Resources() {
			if exportChangedOnly && !changedIds[rb.CurId().String()] {
				continue
			}

			// Format filename: namespace_kind_name.yaml
			ns := rb.GetNamespace()
			if ns == "" {
				ns = "cluster"
			}
			filename := fmt.Sprintf("%s_%s_%s.yaml", ns, rb.GetKind(), rb.GetName())
			filename = filepath.Join(exportDir, filename)

			content := fmt.Sprintf("---\n%s", string(rb.MustYaml()))
			if err := os.WriteFile(filename, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing exported manifest %s: %w", filename, err)
			}
		}
	}

	return nil
}
