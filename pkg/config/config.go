package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tobiash/flux-manifest-preview/pkg/filter"
	"gopkg.in/yaml.v3"
)

var searchPaths = []string{
	".fmp.yaml",
	".fmp.yml",
	".github/fmp.yaml",
}

type Config struct {
	Paths        []string            `yaml:"paths,omitempty"`
	Recursive    *bool               `yaml:"recursive,omitempty"`
	Helm         *bool               `yaml:"helm,omitempty"`
	ResolveGit   *bool               `yaml:"resolve-git,omitempty"`
	SOPSDecrypt  *bool               `yaml:"sops-decrypt,omitempty"`
	Sort         *bool               `yaml:"sort,omitempty"`
	ExcludeCRDs  *bool               `yaml:"exclude-crds,omitempty"`
	Filters      filter.FilterConfig `yaml:",inline"`
	HelmSettings *HelmSettings       `yaml:"helm-settings,omitempty"`
}

type HelmSettings struct {
	RegistryConfig   string `yaml:"registry-config,omitempty"`
	RepositoryConfig string `yaml:"repository-config,omitempty"`
	RepositoryCache  string `yaml:"repository-cache,omitempty"`
}

func DiscoverConfigPath(repoPath string) string {
	for _, p := range searchPaths {
		full := filepath.Join(repoPath, p)
		if _, err := os.Stat(full); err == nil {
			return full
		}
	}
	return ""
}

func LoadConfig(repoPath string) (*Config, error) {
	configPath := DiscoverConfigPath(repoPath)
	if configPath == "" {
		return nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", configPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", configPath, err)
	}

	return &cfg, nil
}

func LoadConfigFromPath(configPath string) (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", configPath, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", configPath, err)
	}

	return &cfg, nil
}

func BoolPtr(b bool) *bool {
	return &b
}

func BoolOr(ptr *bool, def bool) bool {
	if ptr == nil {
		return def
	}
	return *ptr
}
