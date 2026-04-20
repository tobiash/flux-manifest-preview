package preview

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// KustomizationInfo holds extracted fields from a Flux Kustomization CR.
type KustomizationInfo struct {
	Name       string
	Namespace  string
	Path       string
	SourceKind string
	SourceName string
}

// HelmReleaseInfo holds extracted fields from a HelmRelease CR.
type HelmReleaseInfo struct {
	Name       string
	Namespace  string
	Chart      string
	Version    string
	SourceKind string
	SourceName string
}

// ListKustomizations discovers and lists Flux Kustomizations from the repo at path.
func (p *Preview) ListKustomizations(path string) ([]KustomizationInfo, error) {
	r, err := p.loadRepo(path)
	if err != nil {
		return nil, err
	}
	return extractKustomizations(r.render), nil
}

// ListHelmReleases discovers and lists HelmReleases from the repo at path.
func (p *Preview) ListHelmReleases(path string) ([]HelmReleaseInfo, error) {
	r, err := p.loadRepo(path)
	if err != nil {
		return nil, err
	}
	return extractHelmReleases(r.render), nil
}

func extractKustomizations(r *render.Render) []KustomizationInfo {
	target := resid.NewGvk("kustomize.toolkit.fluxcd.io", "v1", "Kustomization")
	var result []KustomizationInfo
	for _, res := range r.Resources() {
		if !matchListGVK(res.GetGvk(), target) {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		spec, _ := m["spec"].(map[string]any)
		path, _, _ := unstructured.NestedString(spec, "path")
		srcRef, _ := spec["sourceRef"].(map[string]any)
		srcKind, _, _ := unstructured.NestedString(srcRef, "kind")
		srcName, _, _ := unstructured.NestedString(srcRef, "name")
		result = append(result, KustomizationInfo{
			Name:       res.GetName(),
			Namespace:  res.GetNamespace(),
			Path:       path,
			SourceKind: srcKind,
			SourceName: srcName,
		})
	}
	return result
}

func extractHelmReleases(r *render.Render) []HelmReleaseInfo {
	target := resid.NewGvk("helm.toolkit.fluxcd.io", "v2", "HelmRelease")
	var result []HelmReleaseInfo
	for _, res := range r.Resources() {
		if !matchListGVK(res.GetGvk(), target) {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		spec, _ := m["spec"].(map[string]any)
		chart := listNestedStr(spec, "chart", "spec", "chart")
		version := listNestedStr(spec, "chart", "spec", "version")
		srcRefRaw, _ := listNestedMap(spec, "chart", "spec", "sourceRef")
		srcRef, _ := srcRefRaw.(map[string]any)
		srcKind, _, _ := unstructured.NestedString(srcRef, "kind")
		srcName, _, _ := unstructured.NestedString(srcRef, "name")
		result = append(result, HelmReleaseInfo{
			Name:       res.GetName(),
			Namespace:  res.GetNamespace(),
			Chart:      chart,
			Version:    version,
			SourceKind: srcKind,
			SourceName: srcName,
		})
	}
	return result
}

func matchListGVK(a, b resid.Gvk) bool {
	return a.Group == b.Group && a.Kind == b.Kind
}

// PrintKustomizations writes a table of Flux Kustomizations to out.
func PrintKustomizations(ks []KustomizationInfo, out io.Writer) {
	if len(ks) == 0 {
		fmt.Fprintln(out, "No Kustomizations found.")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tNAMESPACE\tPATH\tSOURCE")
	for _, k := range ks {
		src := k.SourceKind + "/" + k.SourceName
		if src == "/" {
			src = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.Name, k.Namespace, k.Path, src)
	}
	tw.Flush()
}

// PrintHelmReleases writes a table of HelmReleases to out.
func PrintHelmReleases(hrs []HelmReleaseInfo, out io.Writer) {
	if len(hrs) == 0 {
		fmt.Fprintln(out, "No HelmReleases found.")
		return
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tNAMESPACE\tCHART\tVERSION\tSOURCE")
	for _, hr := range hrs {
		src := hr.SourceKind + "/" + hr.SourceName
		if src == "/" {
			src = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", hr.Name, hr.Namespace, hr.Chart, hr.Version, src)
	}
	tw.Flush()
}

func listNestedStr(m map[string]any, keys ...string) string {
	val, _ := listNestedMap(m, keys...)
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

func listNestedMap(m map[string]any, keys ...string) (any, bool) {
	for i, key := range keys {
		val, ok := m[key]
		if !ok {
			return nil, false
		}
		if i == len(keys)-1 {
			return val, true
		}
		next, ok := val.(map[string]any)
		if !ok {
			return nil, false
		}
		m = next
	}
	return nil, false
}
