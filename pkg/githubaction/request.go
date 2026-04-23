package githubaction

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
)

// Request captures normalized inputs for the GitHub Action mode.
type Request struct {
	// Diff targets
	Repo    string
	BaseRef string
	BaseSHA string
	RepoA   string
	RepoB   string
	Paths   []string

	// Render options
	Recursive        bool
	RenderHelm       bool
	ResolveGit       bool
	Sort             bool
	ExcludeCRDs      bool
	SOPSDecrypt      bool
	HelmRelease      string
	ConfigFile       string
	FilterFile       string
	FilterYAML       string
	RegistryConfig   string
	RepositoryConfig string
	RepositoryCache  string

	// Export
	ExportDir         string
	ExportChangedOnly bool

	// Reporting
	WriteSummary       bool
	Comment            bool
	CommentMode        string // changes, always, failure
	MaxInlineDiffBytes int
	DiffPreviewLines   int

	// Policy
	FailOnWarning bool
	FailOnError   bool
}

// ParseRequestFromEnv builds a Request from GitHub Actions inputs (passed as env vars).
func ParseRequestFromEnv() (*Request, error) {
	r := &Request{
		Repo:               getInput("repo", "."),
		BaseRef:            getInput("base-ref", ""),
		BaseSHA:            getInput("base-sha", ""),
		RepoA:              getInput("repo-a", ""),
		RepoB:              getInput("repo-b", ""),
		Paths:              parseLines(getInput("paths", "")),
		Recursive:          parseBool(getInput("recursive", "false")),
		RenderHelm:         parseBool(getInput("helm", "true")),
		ResolveGit:         parseBool(getInput("resolve-git", "false")),
		Sort:               parseBool(getInput("sort", "false")),
		ExcludeCRDs:        parseBool(getInput("exclude-crds", "false")),
		SOPSDecrypt:        parseBool(getInput("sops-decrypt", "false")),
		HelmRelease:        getInput("helm-release", ""),
		ConfigFile:         getInput("config", ""),
		FilterFile:         getInput("filter-file", ""),
		FilterYAML:         getInput("filter-yaml", ""),
		RegistryConfig:     getInput("registry-config", ""),
		RepositoryConfig:   getInput("repository-config", ""),
		RepositoryCache:    getInput("repository-cache", ""),
		ExportDir:          getInput("export-dir", ""),
		ExportChangedOnly:  parseBool(getInput("export-changed-only", "false")),
		WriteSummary:       parseBool(getInput("write-summary", "true")),
		Comment:            parseBool(getInput("comment", "false")),
		CommentMode:        getInput("comment-mode", "changes"),
		MaxInlineDiffBytes: parseInt(getInput("max-inline-diff-bytes", "50000")),
		DiffPreviewLines:   parseInt(getInput("diff-preview-lines", "200")),
		FailOnWarning:      parseBool(getInput("fail-on-warning", "false")),
		FailOnError:        parseBool(getInput("fail-on-error", "true")),
	}

	if r.SOPSDecrypt {
		return nil, fmt.Errorf("sops-decrypt is intentionally unsupported in GitHub Action mode to avoid leaking decrypted content into logs, summaries, comments, or artifacts")
	}

	if r.RepoA != "" && r.RepoB != "" && len(r.Paths) == 0 {
		// Legacy mode: normalize to Repo/RepoA/RepoB fields
		r.Paths = parseLines(getInput("kustomizations", ""))
	}

	return r, nil
}

// DiffLeft returns the base/left path or ref for diffing.
func (r *Request) DiffLeft() string {
	if r.RepoA != "" {
		return r.RepoA
	}
	if r.BaseRef != "" {
		return r.BaseRef
	}
	if r.BaseSHA != "" {
		return r.BaseSHA
	}
	return "HEAD"
}

// DiffRight returns the target/right path or ref for diffing.
func (r *Request) DiffRight() string {
	if r.RepoB != "" {
		return r.RepoB
	}
	return r.Repo
}

// HasConfig returns true if an explicit config file or auto-discoverable config should be used.
func (r *Request) HasConfig() bool {
	return r.ConfigFile != "" || config.DiscoverConfigPath(r.ConfigRoot()) != ""
}

// ConfigRoot returns the directory to search for .fmp.yaml.
func (r *Request) ConfigRoot() string {
	if r.ConfigFile != "" {
		return ""
	}
	if r.RepoA != "" {
		return r.RepoA
	}
	return r.Repo
}

// IsLegacy returns true when the legacy two-repo mode is used.
func (r *Request) IsLegacy() bool {
	return r.RepoA != "" && r.RepoB != ""
}

func getInput(name, def string) string {
	underscore := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	v := os.Getenv("INPUT_" + underscore)
	if v == "" && strings.Contains(name, "-") {
		hyphen := strings.ToUpper(name)
		v = os.Getenv("INPUT_" + hyphen)
	}
	if v == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func parseBool(v string) bool {
	b, _ := strconv.ParseBool(v)
	return b
}

func parseInt(v string) int {
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

func parseLines(v string) []string {
	if v == "" {
		return nil
	}
	lines := strings.Split(v, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
