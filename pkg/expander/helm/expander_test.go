package helm

import (
	"bytes"
	"context"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/kustomize/api/resmap"
)

func TestBuildPostRenderersFromSpec(t *testing.T) {
	spec := map[string]any{
		"commonMetadata": map[string]any{
			"labels": map[string]any{"env": "prod"},
		},
		"postRenderers": []any{
			map[string]any{
				"kustomize": map[string]any{
					"patches": []any{
						map[string]any{
							"patch": `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
  labels:
    patched: "true"
`,
						},
					},
				},
			},
		},
	}

	renderer, err := buildPostRenderersFromSpec("release", "default", spec)
	if err != nil {
		t.Fatalf("buildPostRenderersFromSpec() error = %v", err)
	}

	out, err := renderer.Run(bytes.NewBufferString(`apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !bytes.Contains([]byte(result), []byte("patched: \"true\"")) {
		t.Fatalf("expected patched label in output, got:\n%s", result)
	}
	if !bytes.Contains([]byte(result), []byte("env: prod")) {
		t.Fatalf("expected common metadata label in output, got:\n%s", result)
	}
	if !bytes.Contains([]byte(result), []byte("helm.toolkit.fluxcd.io/name: release")) {
		t.Fatalf("expected origin label in output, got:\n%s", result)
	}
}

func TestRenderAllCharts_PassesPostRendererToRunner(t *testing.T) {
	runner := &stubChartRunner{}
	s := &expandState{
		runner: runner,
		render: render.NewDefaultRender(logr.Discard()),
		logger: logr.Discard(),
		releases: []*helmv2.HelmRelease{{
			ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "default"},
			Spec: helmv2.HelmReleaseSpec{
				Chart: &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{
					Chart: "podinfo",
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind: "HelmRepository",
						Name: "podinfo",
					},
				}},
				CommonMetadata: &helmv2.CommonMetadata{Labels: map[string]string{"env": "prod"}},
			},
		}},
		repositories: []*sourcev1.HelmRepository{{
			ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "default"},
			Spec:       sourcev1.HelmRepositorySpec{URL: "https://example.com/charts"},
		}},
	}

	_, errs, err := s.renderAllCharts(context.Background())
	if err != nil {
		t.Fatalf("renderAllCharts() error = %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("renderAllCharts() returned unexpected warnings: %v", errs)
	}
	if len(runner.tasks) != 1 {
		t.Fatalf("expected one render task, got %d", len(runner.tasks))
	}
	if runner.tasks[0].postRenderer == nil {
		t.Fatal("expected render task to include a post renderer")
	}

	out, err := runPostRenderer(runner.tasks[0].postRenderer, bytes.NewBufferString(`apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`))
	if err != nil {
		t.Fatalf("runPostRenderer() error = %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("env: prod")) {
		t.Fatalf("expected task post renderer to add common metadata, got:\n%s", out.String())
	}
}

func TestRenderAllCharts_ResolvesGitRepositoryChartSource(t *testing.T) {
	runner := &stubChartRunner{}
	s := &expandState{
		runner:   runner,
		resolver: stubChartSourceResolver{"flux-system/podinfo": "/tmp/source"},
		render:   render.NewDefaultRender(logr.Discard()),
		logger:   logr.Discard(),
		releases: []*helmv2.HelmRelease{{
			ObjectMeta: metav1.ObjectMeta{Name: "podinfo", Namespace: "default"},
			Spec: helmv2.HelmReleaseSpec{
				Chart: &helmv2.HelmChartTemplate{Spec: helmv2.HelmChartTemplateSpec{
					Chart: "./charts/podinfo",
					SourceRef: helmv2.CrossNamespaceObjectReference{
						Kind:      "GitRepository",
						Name:      "podinfo",
						Namespace: "flux-system",
					},
				}},
			},
		}},
	}

	_, errs, err := s.renderAllCharts(context.Background())
	if err != nil {
		t.Fatalf("renderAllCharts() error = %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("renderAllCharts() returned unexpected warnings: %v", errs)
	}
	if len(runner.tasks) != 1 {
		t.Fatalf("expected one render task, got %d", len(runner.tasks))
	}
	if got, want := runner.tasks[0].localChartPath, "/tmp/source/charts/podinfo"; got != want {
		t.Fatalf("localChartPath = %q, want %q", got, want)
	}
}

type stubChartRunner struct {
	tasks []RenderTask
}

type stubChartSourceResolver map[string]string

func (s stubChartSourceResolver) ResolvePath(namespace, name string) (string, bool) {
	path, ok := s[namespace+"/"+name]
	return path, ok
}

func (s *stubChartRunner) RenderCharts(_ context.Context, tasks []RenderTask) (resmap.ResMap, []error, error) {
	s.tasks = append([]RenderTask(nil), tasks...)
	return resmap.New(), nil, nil
}
