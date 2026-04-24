package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
)

func TestEvaluateBuiltinsAndMappings(t *testing.T) {
	result, err := Evaluate(context.Background(), &diff.DiffResult{
		Modified: []diff.ResourceChange{{
			Action:    "modified",
			Kind:      "Deployment",
			Name:      "web",
			Namespace: "default",
			Old:       map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "nginx:1.0"}}}}}},
			New:       map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "nginx:1.1"}}}}}},
		}},
	}, &config.PolicyConfig{
		Builtin: []string{"image_update"},
		Labels:  map[string]config.LabelList{"image_update": {"image-update"}},
	}, t.TempDir())
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(result.Classifications) != 1 || result.Classifications[0].ID != "image_update" {
		t.Fatalf("expected image_update classification, got %+v", result.Classifications)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "image-update" {
		t.Fatalf("expected mapped label, got %v", result.Labels)
	}
}

func TestEvaluateCustomModulesAndFailOn(t *testing.T) {
	dir := t.TempDir()
	modulePath := filepath.Join(dir, "policy.rego")
	if err := os.WriteFile(modulePath, []byte(`package fmp
import rego.v1

violations contains {
  "id": "forbid_latest",
  "message": "latest tag is forbidden",
  "severity": "error"
} if {
  some change in input.changes
  change.new.spec.template.spec.containers[_].image == "nginx:latest"
}

labels contains "needs-manual-review" if true
`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := Evaluate(context.Background(), &diff.DiffResult{
		Modified: []diff.ResourceChange{{
			Action:    "modified",
			Kind:      "Deployment",
			Name:      "web",
			Namespace: "default",
			New:       map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"image": "nginx:latest"}}}}}},
		}},
	}, &config.PolicyConfig{
		Modules: []string{"*.rego"},
		FailOn:  []string{"forbid_latest"},
	}, dir)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !result.PolicyFailed {
		t.Fatal("expected policy failure")
	}
	if len(result.PolicyFailures) != 1 || result.PolicyFailures[0] != "forbid_latest" {
		t.Fatalf("expected forbid_latest failure, got %v", result.PolicyFailures)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "needs-manual-review" {
		t.Fatalf("expected direct label from policy, got %v", result.Labels)
	}
}
