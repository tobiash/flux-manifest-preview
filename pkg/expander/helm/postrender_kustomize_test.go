package helm

import (
	"bytes"
	"testing"

	v2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/kustomize"
)

func TestPostRendererKustomize_Patches(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: default
data:
  key: original
`
	spec := &v2.Kustomize{
		Patches: []kustomize.Patch{
			{
				Patch: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
data:
  key: patched
`,
			},
		},
	}

	renderer := newPostRendererKustomize(spec)
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !bytes.Contains([]byte(result), []byte("patched")) {
		t.Errorf("expected patched value in output, got:\n%s", result)
	}
}

func TestPostRendererKustomize_Images(t *testing.T) {
	input := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deploy
spec:
  template:
    spec:
      containers:
        - name: app
          image: myapp:v1
`
	spec := &v2.Kustomize{
		Images: []kustomize.Image{
			{
				Name:   "myapp",
				NewTag: "v2",
			},
		},
	}

	renderer := newPostRendererKustomize(spec)
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !bytes.Contains([]byte(result), []byte("myapp:v2")) && !bytes.Contains([]byte(result), []byte("v2")) {
		t.Errorf("expected image tag to be updated, got:\n%s", result)
	}
}

func TestPostRendererKustomize_EmptySpec(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
data:
  key: value
`
	spec := &v2.Kustomize{}
	renderer := newPostRendererKustomize(spec)
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !bytes.Contains([]byte(result), []byte("key: value")) {
		t.Errorf("expected unchanged output for empty kustomize spec, got:\n%s", result)
	}
}

func TestAdaptImages(t *testing.T) {
	images := []kustomize.Image{
		{Name: "app", NewName: "myapp", NewTag: "v2", Digest: "sha256:abc"},
	}
	result := adaptImages(images)
	if len(result) != 1 {
		t.Fatalf("expected 1 image, got %d", len(result))
	}
	if result[0].Name != "app" || result[0].NewName != "myapp" || result[0].NewTag != "v2" || result[0].Digest != "sha256:abc" {
		t.Errorf("unexpected image conversion: %+v", result[0])
	}
}
