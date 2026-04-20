package helm

import (
	"bytes"
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
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
		releases: []unstructuredRelease{{
			name:      "podinfo",
			namespace: "default",
			spec: map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{
						"chart": "podinfo",
						"sourceRef": map[string]any{
							"kind": "HelmRepository",
							"name": "podinfo",
						},
					},
				},
				"commonMetadata": map[string]any{
					"labels": map[string]any{"env": "prod"},
				},
			},
		}},
		repositories: []unstructuredRepository{{
			name:      "podinfo",
			namespace: "default",
			url:       "https://example.com/charts",
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

type stubChartRunner struct {
	tasks []RenderTask
}

func (s *stubChartRunner) ResolveVersion(_, _, version string) (string, error) {
	return version, nil
}

func (s *stubChartRunner) RenderCharts(_ context.Context, tasks []RenderTask) (resmap.ResMap, []error, error) {
	s.tasks = append([]RenderTask(nil), tasks...)
	return resmap.New(), nil, nil
}
