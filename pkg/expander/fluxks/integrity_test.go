package fluxks

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
)

func TestExpandReportsMissingAndInvalidSources(t *testing.T) {
	for _, spec := range []string{
		"path: apps\n  sourceRef:\n    kind: OCIRepository\n    name: source",
		"path: [invalid]",
	} {
		r := renderFromYAML(t, "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: apps\n  namespace: flux-system\nspec:\n  "+spec+"\n")
		result, err := NewExpanderWithResolver(logr.Discard(), stubResolver{}).Expand(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Errors) != 1 || len(result.DiscoveredPaths) != 0 {
			t.Errorf("Expand(%q) = %+v, want error and no local fallback", spec, result)
		}
	}
}

func TestExpandDiscoversSourceRoot(t *testing.T) {
	for _, spec := range []string{"path: ./", "prune: true"} {
		r := renderFromYAML(t, "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: root\nspec:\n  "+spec+"\n")
		result, err := NewExpander(logr.Discard()).Expand(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.DiscoveredPaths) != 1 || result.DiscoveredPaths[0].Path != "." {
			t.Errorf("Expand(root) = %+v, want root build", result)
		}
	}
}

func TestExpandDefersMissingSource(t *testing.T) {
	r := renderFromYAML(t, "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: app\n  namespace: flux-system\nspec:\n  sourceRef:\n    kind: GitRepository\n    name: late\n")
	resolver := stubResolver{}
	e := NewExpanderWithResolver(logr.Discard(), resolver)
	result, err := e.Expand(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 || len(result.DeferredErrors) != 1 || len(result.DiscoveredPaths) != 0 {
		t.Fatalf("Expand(missing source) = %+v, want deferred error only", result)
	}
	resolver["flux-system/late"] = "/source"
	result, err = e.Expand(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 || len(result.DeferredErrors) != 0 || len(result.DiscoveredPaths) != 1 {
		t.Fatalf("Expand(discovered source) = %+v, want path without errors", result)
	}
}
