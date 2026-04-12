package filter

import (
	"testing"

	"sigs.k8s.io/kustomize/kyaml/yaml"
)

func TestFilterConfig_UnmarshalLabelRemover(t *testing.T) {
	input := `
filters:
  - kind: LabelRemover
    labels:
      - app.kubernetes.io/managed-by
`
	var fc FilterConfig
	if err := yaml.Unmarshal([]byte(input), &fc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(fc.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(fc.Filters))
	}
	lr, ok := fc.Filters[0].Filter.(*LabelRemover)
	if !ok {
		t.Fatal("expected LabelRemover filter")
	}
	if len(lr.Labels) != 1 || lr.Labels[0] != "app.kubernetes.io/managed-by" {
		t.Errorf("expected labels [app.kubernetes.io/managed-by], got %v", lr.Labels)
	}
}

func TestFilterConfig_UnmarshalUnknownKind(t *testing.T) {
	input := `
filters:
  - kind: DoesNotExist
`
	var fc FilterConfig
	err := yaml.Unmarshal([]byte(input), &fc)
	if err == nil {
		t.Fatal("expected error for unknown filter kind")
	}
}

func TestFilterConfig_UnmarshalMultipleFilters(t *testing.T) {
	input := `
filters:
  - kind: LabelRemover
    labels:
      - foo
      - bar
  - kind: LabelRemover
    labels:
      - baz
`
	var fc FilterConfig
	if err := yaml.Unmarshal([]byte(input), &fc); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(fc.Filters) != 2 {
		t.Fatalf("expected 2 filters, got %d", len(fc.Filters))
	}
}
