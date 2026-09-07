package render

import (
	"testing"

	"github.com/go-logr/logr"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestMalformedRawYAMLReturnsError(t *testing.T) {
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/raw"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/raw/bad.yaml", []byte("apiVersion: v1\nkind: ConfigMap\nmetadata: [")); err != nil {
		t.Fatal(err)
	}
	if err := NewDefaultRender(logr.Discard()).AddPath(fs, "/raw"); err == nil {
		t.Fatal("AddPath(malformed YAML) succeeded, want error")
	}
}

func TestRawYAMLExcludesOnlyKnownToolConfig(t *testing.T) {
	for _, name := range []string{".fmp.yaml", ".fmp.yml", ".github/fmp.yaml"} {
		t.Run(name, func(t *testing.T) {
			fs := filesys.MakeFsInMemory()
			if err := fs.MkdirAll("/raw/.github"); err != nil {
				t.Fatal(err)
			}
			if err := fs.WriteFile("/raw/"+name, []byte("sort: true\n")); err != nil {
				t.Fatal(err)
			}
			if err := fs.WriteFile("/raw/cm.yaml", []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")); err != nil {
				t.Fatal(err)
			}
			r := NewDefaultRender(logr.Discard())
			if err := r.AddPaths(fs, "/raw"); err != nil {
				t.Fatalf("AddPaths(tool config %s) = %v", name, err)
			}
			if r.Size() != 1 {
				t.Errorf("AddPaths rendered %d resources, want 1", r.Size())
			}
			if err := fs.WriteFile("/raw/fmp.yaml", []byte("sort: true\n")); err != nil {
				t.Fatal(err)
			}
			if err := NewDefaultRender(logr.Discard()).AddPaths(fs, "/raw"); err == nil {
				t.Fatal("AddPaths(non-config invalid YAML) succeeded, want error")
			}
		})
	}
}

func TestNamespaceTransformReportsCollisionsAndPreservesClusterScope(t *testing.T) {
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/raw"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/raw/resources.yaml", []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: first
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: shared
  namespace: second
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Cluster
  names:
    kind: Widget
    plural: widgets
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: cluster-widget
`)); err != nil {
		t.Fatal(err)
	}
	r := NewDefaultRender(logr.Discard())
	if err := r.AddPathWithProducer(fs, "/raw", "Kustomization flux/apps"); err != nil {
		t.Fatal(err)
	}
	if err := r.ApplyNamespaceToNew(0, "target"); err != nil {
		t.Fatal(err)
	}
	if len(r.Warnings()) != 1 {
		t.Errorf("namespace collision warnings = %v, want one", r.Warnings())
	}
	for _, res := range r.Resources() {
		want := ""
		if res.GetKind() == "ConfigMap" {
			want = "target"
		}
		if got := res.GetNamespace(); got != want {
			t.Errorf("%s namespace = %q, want %q", res.GetKind(), got, want)
		}
		if got := r.ProvenanceForID(res.CurId()).String(); got != "Kustomization flux/apps" {
			t.Errorf("transformed provenance = %q", got)
		}
	}
	merged := NewDefaultRender(logr.Discard())
	if err := merged.AbsorbAll(r); err != nil {
		t.Fatal(err)
	}
	if len(merged.Warnings()) != 1 {
		t.Errorf("merged warnings = %v, want namespace collision", merged.Warnings())
	}
}
