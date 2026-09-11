package render

import (
	"testing"

	"sigs.k8s.io/kustomize/kyaml/yaml"
)

func TestAsJSONRejectsInvalidResourceMap(t *testing.T) {
	r := newRenderFromYAML(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: broken\n")
	node := r.Resources()[0].YNode()
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "broken"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "not-an-int"})
	if _, err := r.Resources()[0].Map(); err == nil {
		t.Fatal("invalid map fixture unexpectedly serialized")
	}
	data, err := r.AsJSON()
	if err == nil || len(data) != 0 {
		t.Fatalf("AsJSON(invalid map) = %s, error=%v; want error without output", data, err)
	}
}
