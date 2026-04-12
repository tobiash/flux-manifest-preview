package filter

import (
	"testing"

	"sigs.k8s.io/kustomize/kyaml/yaml"
)

func TestLabelRemover_RemovesLabels(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
  labels:
    app: myapp
    helm.toolkit.fluxcd.io/name: myrelease
    helm.toolkit.fluxcd.io/namespace: default
data:
  key: value
`),
	}

	lr := LabelRemover{
		Labels: []string{"helm.toolkit.fluxcd.io/name", "helm.toolkit.fluxcd.io/namespace"},
	}

	result, err := lr.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if contains(got, "helm.toolkit.fluxcd.io/name") {
		t.Error("expected origin label 'name' to be removed")
	}
	if contains(got, "helm.toolkit.fluxcd.io/namespace") {
		t.Error("expected origin label 'namespace' to be removed")
	}
	if !contains(got, "app: myapp") {
		t.Error("expected 'app' label to be preserved")
	}
}

func TestLabelRemover_CustomPaths(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
  labels:
    remove-me: "true"
spec:
  template:
    metadata:
      labels:
        remove-me: "true"
        keep-me: "true"
`),
	}

	lr := LabelRemover{
		Labels: []string{"remove-me"},
		Paths:  [][]string{{"metadata", "labels"}, {"spec", "template", "metadata", "labels"}},
	}

	result, err := lr.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if contains(got, "remove-me") {
		t.Error("expected 'remove-me' to be removed from all specified paths")
	}
	if !contains(got, "keep-me") {
		t.Error("expected 'keep-me' to be preserved")
	}
}

func TestLabelRemover_NoLabelsToRemove(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`),
	}

	lr := LabelRemover{Labels: []string{"does-not-exist"}}
	result, err := lr.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
}

func parseRNode(t *testing.T, s string) *yaml.RNode {
	t.Helper()
	n, err := yaml.Parse(s)
	if err != nil {
		t.Fatalf("failed to parse yaml: %v", err)
	}
	return n
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
