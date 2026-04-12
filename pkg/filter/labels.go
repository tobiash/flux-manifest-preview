package filter

import (
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

var _ kio.Filter = &LabelRemover{}

// LabelRemover removes specified labels from resources at given field paths.
// If no paths are specified, it defaults to metadata/labels and
// spec/template/metadata/labels.

type LabelRemover struct {
	Kind   string     `yaml:"kind,omitempty"`
	Labels []string   `yaml:"labels"`
	Paths  [][]string `yaml:"paths"`
}

func (lr LabelRemover) Filter(input []*yaml.RNode) ([]*yaml.RNode, error) {
	lps := lr.Paths
	if len(lps) == 0 {
		lps = [][]string{
			{"metadata", "labels"},
			{"spec", "template", "metadata", "labels"},
		}
	}
	for i := range input {
		node := input[i]
		for _, l := range lr.Labels {
			for _, lp := range lps {
				_, err := node.Pipe(
					&yaml.PathGetter{Path: lp},
					&yaml.FieldClearer{Name: l},
				)
				if err != nil {
					return nil, err
				}
			}
		}
	}
	return input, nil
}
