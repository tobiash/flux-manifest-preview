package diff

import (
	"bytes"
	"testing"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/api/hasher"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
)

func TestDiff_AddedResource(t *testing.T) {
	a := render.NewDefaultRender(logr.Discard())
	b := makeRender(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: new-cm
  namespace: default
data:
  key: value
`)

	var buf bytes.Buffer
	if err := Diff(a, b, &buf); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty diff output for added resource")
	}
	if !bytes.Contains(buf.Bytes(), []byte("new-cm")) {
		t.Error("expected diff to mention 'new-cm'")
	}
}

func TestDiff_DeletedResource(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: old-cm
  namespace: default
data:
  key: value
`)
	b := render.NewDefaultRender(logr.Discard())

	var buf bytes.Buffer
	if err := Diff(a, b, &buf); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty diff output for deleted resource")
	}
	if !bytes.Contains(buf.Bytes(), []byte("old-cm")) {
		t.Error("expected diff to mention 'old-cm'")
	}
}

func TestDiff_ModifiedResource(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  key: old-value
`)
	b := makeRender(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  key: new-value
`)

	var buf bytes.Buffer
	if err := Diff(a, b, &buf); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if buf.Len() == 0 {
		t.Error("expected non-empty diff output for modified resource")
	}
	if !bytes.Contains(buf.Bytes(), []byte("old-value")) {
		t.Error("expected diff to contain 'old-value'")
	}
	if !bytes.Contains(buf.Bytes(), []byte("new-value")) {
		t.Error("expected diff to contain 'new-value'")
	}
}

func TestDiff_IdenticalResources(t *testing.T) {
	yaml := `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  key: same
`
	a := makeRender(t, yaml)
	b := makeRender(t, yaml)

	var buf bytes.Buffer
	if err := Diff(a, b, &buf); err != nil {
		t.Fatalf("Diff() error = %v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("expected empty diff for identical resources, got:\n%s", buf.String())
	}
}

func makeRender(t *testing.T, yaml string) *render.Render {
	t.Helper()
	r := render.NewDefaultRender(logr.Discard())
	resFactory := resource.NewFactory(&hasher.Hasher{})
	rmFactory := resmap.NewFactory(resFactory)

	rm, err := rmFactory.NewResMapFromBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create resmap: %v", err)
	}
	if err := r.AbsorbAll(rm); err != nil {
		t.Fatalf("failed to absorb: %v", err)
	}
	return r
}
