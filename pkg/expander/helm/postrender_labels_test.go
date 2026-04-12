package helm

import (
	"bytes"
	"testing"

	v2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPostRendererOriginLabels(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: default
data:
  key: value
`
	renderer := newPostRendererOriginLabels("myrelease", "mynamespace")

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "helm.toolkit.fluxcd.io/name: myrelease") {
		t.Errorf("expected origin name label, got:\n%s", result)
	}
	if !contains(result, "helm.toolkit.fluxcd.io/namespace: mynamespace") {
		t.Errorf("expected origin namespace label, got:\n%s", result)
	}
}

func TestPostRendererOriginLabels_MultipleResources(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: cm1
---
apiVersion: v1
kind: Secret
metadata:
  name: secret1
`
	renderer := newPostRendererOriginLabels("rel", "ns")
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	count := bytes.Count([]byte(result), []byte("helm.toolkit.fluxcd.io/name: rel"))
	if count != 2 {
		t.Errorf("expected 2 origin name labels, got %d", count)
	}
}

func TestPostRendererCommonMetadata_Labels(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	cm := &v2.CommonMetadata{
		Labels:      map[string]string{"app": "test", "env": "prod"},
		Annotations: nil,
	}
	renderer := newPostRendererCommonMetadata(cm)

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "app: test") {
		t.Errorf("expected 'app: test' label, got:\n%s", result)
	}
	if !contains(result, "env: prod") {
		t.Errorf("expected 'env: prod' label, got:\n%s", result)
	}
}

func TestPostRendererCommonMetadata_NilBoth(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	cm := &v2.CommonMetadata{}
	renderer := newPostRendererCommonMetadata(cm)

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if out.String() != input {
		t.Errorf("expected unchanged output when commonMetadata is empty")
	}
}

func TestBuildPostRenderers_IncludesOriginLabels(t *testing.T) {
	hr := &v2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
	}
	renderer := buildPostRenderers(hr)
	if renderer == nil {
		t.Fatal("expected non-nil post-renderer (at minimum origin labels)")
	}

	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !contains(out.String(), "helm.toolkit.fluxcd.io/name: test") {
		t.Error("expected origin name label in output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}
