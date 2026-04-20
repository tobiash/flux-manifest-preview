package filter

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/kustomize/kyaml/kio"
	kiofilters "sigs.k8s.io/kustomize/kyaml/kio/filters"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

var Filters = map[string]func() kio.Filter{
	"FileSetter":      func() kio.Filter { return &kiofilters.FileSetter{} },
	"FormatFilter":    func() kio.Filter { return &kiofilters.FormatFilter{} },
	"GrepFilter":      func() kio.Filter { return &kiofilters.GrepFilter{} },
	"MatchModifier":   func() kio.Filter { return &kiofilters.MatchModifyFilter{} },
	"Modifier":        func() kio.Filter { return &kiofilters.Modifier{} },
	"LabelRemover":    func() kio.Filter { return &LabelRemover{} },
	"FieldNormalizer": func() kio.Filter { return &FieldNormalizer{} },
}

// KFilter wraps a kio.Filter for YAML marshaling and unmarshaling.

type KFilter struct {
	kio.Filter
}

// FilterConfig defines a pipeline of KIO filters to apply.

type FilterConfig struct {
	Kind    string    `yaml:"kind,omitempty"`
	Filters []KFilter `yaml:"filters,omitempty"`
}

func (f KFilter) MarshalYAML() (any, error) {
	return f.Filter, nil
}

func (f *KFilter) UnmarshalYAML(unmarshal func(any) error) error {
	i := map[string]any{}
	if err := unmarshal(i); err != nil {
		return err
	}
	meta := &yaml.ResourceMeta{}
	if err := unmarshal(meta); err != nil {
		return err
	}
	filter, found := Filters[meta.Kind]
	if !found {
		var knownFilters []string
		for k := range Filters {
			knownFilters = append(knownFilters, k)
		}
		sort.Strings(knownFilters)
		return fmt.Errorf("unsupported filter Kind %v: may be one of: [%s]",
			meta, strings.Join(knownFilters, ","))
	}
	f.Filter = filter()

	return unmarshal(f.Filter)
}
