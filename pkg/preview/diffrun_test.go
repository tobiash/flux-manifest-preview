package preview

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	helmcli "helm.sh/helm/v4/pkg/cli"
)

func TestRunDiffKeepsPartialResultWhenHelmExpansionWarns(t *testing.T) {
	left := t.TempDir()
	right := t.TempDir()

	writePreviewFile(t, left, "helmrelease.yaml", brokenHelmReleaseYAML())
	writePreviewFile(t, right, "helmrelease.yaml", brokenHelmReleaseYAML())
	writePreviewFile(t, right, "configmap.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: new-config
  namespace: default
data:
  key: value
`)

	p, err := New(
		WithLogger(logr.Discard()),
		WithPaths([]string{"."}, false),
		WithHelm(helmcli.New()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	run, err := p.RunDiff(context.Background(), DiffRunOptions{LeftPath: left, RightPath: right})
	if err != nil {
		t.Fatalf("RunDiff() error = %v", err)
	}
	if run.Summary.Total != 1 {
		t.Fatalf("Summary.Total = %d, want 1", run.Summary.Total)
	}
	if len(run.Warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", run.Warnings)
	}
	if !strings.Contains(run.Warnings[0], "HelmRelease default/broken") {
		t.Fatalf("warning %q does not mention broken HelmRelease", run.Warnings[0])
	}
}

func brokenHelmReleaseYAML() string {
	return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: broken
  namespace: default
spec:
  interval: 30m
  chart:
    spec:
      chart: missing
      sourceRef:
        kind: HelmRepository
        name: missing
        namespace: default
`
}
