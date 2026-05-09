package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	fluxksexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/fluxks"
	gitrepoexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/gitrepo"
	helmexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/helm"
	"github.com/tobiash/flux-manifest-preview/pkg/filter"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
	helmcli "helm.sh/helm/v4/pkg/cli"
)

// Preview renders and diffs Flux GitOps resources.
type Preview struct {
	paths           []string
	clusterPaths    map[string][]string
	recursive       bool
	sortOutput      bool
	excludeCRDs     bool
	helmReleaseName string
	sopsDecrypt     bool
	filters         *filter.FilterConfig
	fluxKSEnabled   bool
	gitRepoExpander *gitrepoexpander.Expander
	helmSettings    *helmcli.EnvSettings
	log             logr.Logger
}

func (p *Preview) isClustered() bool {
	return p.clusterPaths != nil
}

// ExpansionError is returned when one or more non-fatal errors were
// encountered during expansion (e.g. a HelmRelease whose chart could
// not be resolved). The render/diff output is still produced but may
// be incomplete.
type ExpansionError struct {
	Errors   []error
	Warnings []error
}

func (e *ExpansionError) Error() string {
	var msgs []string
	for _, err := range e.Errors {
		msgs = append(msgs, err.Error())
	}
	return fmt.Sprintf("expansion errors: %s", strings.Join(msgs, "; "))
}

func sortedClusterNames(results map[string]*loadRepoResult) []string {
	clusters := make([]string, 0, len(results))
	for cluster := range results {
		clusters = append(clusters, cluster)
	}
	sort.Strings(clusters)
	return clusters
}

// Render renders the resources at path and writes the YAML output.
func (p *Preview) Render(ctx context.Context, path string, out io.Writer) error {
	results, err := p.loadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	if p.isClustered() {
		_, _ = fmt.Fprintln(out, "# Rendered manifests")
		clusters := sortedClusterNames(results)
		for _, cluster := range clusters {
			result := results[cluster]
			p.applyOutputOptions(result.render)
			_, _ = fmt.Fprintf(out, "\n---\n# cluster: %s\n---\n", cluster)
			yaml, err := result.render.AsYaml()
			if err != nil {
				return fmt.Errorf("error transforming cluster %q to yaml: %w", cluster, err)
			}
			if _, err := out.Write(yaml); err != nil {
				return fmt.Errorf("error writing output: %w", err)
			}
		}
		var allErrors []error
		for _, cluster := range clusters {
			allErrors = append(allErrors, results[cluster].errors...)
		}
		if len(allErrors) > 0 {
			return &ExpansionError{Errors: allErrors}
		}
		return nil
	}
	result := results[""]
	p.applyOutputOptions(result.render)
	yaml, err := result.render.AsYaml()
	if err != nil {
		return fmt.Errorf("error transforming to yaml: %w", err)
	}
	if _, err := out.Write(yaml); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	if len(result.errors) > 0 {
		return &ExpansionError{Errors: result.errors, Warnings: result.warnings}
	}
	return nil
}

// RenderJSON renders the resources at path and writes JSON output.
func (p *Preview) RenderJSON(ctx context.Context, path string, out io.Writer) error {
	results, err := p.loadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	if p.isClustered() {
		items := make([]map[string]any, 0)
		clusters := sortedClusterNames(results)
		for _, cluster := range clusters {
			result := results[cluster]
			p.applyOutputOptions(result.render)
			for _, res := range result.render.Resources() {
				m, err := res.Map()
				if err != nil {
					continue
				}
				m["_fmp_cluster"] = cluster
				items = append(items, m)
			}
		}
		list := map[string]any{
			"apiVersion": "v1",
			"kind":       "List",
			"items":      items,
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(list); err != nil {
			return fmt.Errorf("error encoding json: %w", err)
		}
		var allErrors []error
		for _, cluster := range clusters {
			allErrors = append(allErrors, results[cluster].errors...)
		}
		if len(allErrors) > 0 {
			return &ExpansionError{Errors: allErrors}
		}
		return nil
	}
	result := results[""]
	p.applyOutputOptions(result.render)
	jsonData, err := result.render.AsJSON()
	if err != nil {
		return fmt.Errorf("error transforming to json: %w", err)
	}
	if _, err := out.Write(jsonData); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	if len(result.errors) > 0 {
		return &ExpansionError{Errors: result.errors, Warnings: result.warnings}
	}
	return nil
}

// TestResult is the JSON representation of a test run.
type TestResult struct {
	Status   string      `json:"status"`
	Warnings []TestIssue `json:"warnings,omitempty"`
	Errors   []TestIssue `json:"errors,omitempty"`
}

// TestIssue describes a single warning or error encountered during testing.
type TestIssue struct {
	Message string `json:"message"`
}

// Test validates that all Kustomizations build and HelmReleases render.
// Returns nil on success, or an error describing the failure.
func (p *Preview) Test(ctx context.Context, path string, out io.Writer) error {
	results, err := p.loadRepo(ctx, path)
	if err != nil {
		_, _ = fmt.Fprintf(out, "FAIL: %v\n", err)
		return err
	}
	var allErrors []error
	clusters := sortedClusterNames(results)
	for _, cluster := range clusters {
		result := results[cluster]
		if p.isClustered() {
			_, _ = fmt.Fprintf(out, "Cluster %q: ", cluster)
		}
		if len(result.errors) > 0 {
			for _, e := range result.errors {
				_, _ = fmt.Fprintf(out, "WARN: %v\n", e)
			}
			_, _ = fmt.Fprintln(out, "PASS (with warnings)")
		} else {
			_, _ = fmt.Fprintln(out, "PASS")
		}
		allErrors = append(allErrors, result.errors...)
	}
	if len(allErrors) > 0 {
		return &ExpansionError{Errors: allErrors}
	}
	return nil
}

// TestJSON validates resources and returns a structured test result.
func (p *Preview) TestJSON(ctx context.Context, path string) (*TestResult, error) {
	results, err := p.loadRepo(ctx, path)
	if err != nil {
		return &TestResult{
			Status: "fail",
			Errors: []TestIssue{{Message: err.Error()}},
		}, err
	}
	var warnings []TestIssue
	for _, cluster := range sortedClusterNames(results) {
		for _, e := range results[cluster].errors {
			warnings = append(warnings, TestIssue{Message: e.Error()})
		}
	}
	if len(warnings) > 0 {
		return &TestResult{Status: "pass_with_warnings", Warnings: warnings}, nil
	}
	return &TestResult{Status: "pass"}, nil
}

// Diff computes and writes the diff between two repository paths.
// If a HelmRelease filter is set, only resources from that release are included.
func (p *Preview) Diff(ctx context.Context, a, b string, out io.Writer) error {
	if p.isClustered() {
		_, err := p.DiffResult(ctx, a, b, out)
		return err
	}

	g, _ := errgroup.WithContext(ctx)
	var ar, br *loadRepoResult
	g.Go(func() error {
		results, err := p.freshLoadRepo(ctx, a)
		if err != nil {
			return err
		}
		ar = results[""]
		return nil
	})
	g.Go(func() error {
		results, err := p.freshLoadRepo(ctx, b)
		if err != nil {
			return err
		}
		br = results[""]
		return nil
	})
	if err := g.Wait(); err != nil {
		return fmt.Errorf("render error: %w", err)
	}

	if p.helmReleaseName != "" {
		ar.render.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
		br.render.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
	}

	p.applyOutputOptions(ar.render)
	p.applyOutputOptions(br.render)
	if err := diff.Diff(ar.render, br.render, out); err != nil {
		return fmt.Errorf("diff error: %w", err)
	}
	var allErrors []error
	seen := make(map[string]bool)
	for _, e := range append(ar.errors, br.errors...) {
		msg := e.Error()
		if !seen[msg] {
			seen[msg] = true
			allErrors = append(allErrors, e)
		}
	}
	if len(allErrors) > 0 {
		return &ExpansionError{Errors: allErrors}
	}
	return nil
}

// DiffResult computes and writes the diff between two repository paths,
// returning structured change metadata alongside the rendered diff text.
func (p *Preview) DiffResult(ctx context.Context, a, b string, out io.Writer) (*diff.DiffResult, error) {
	g, _ := errgroup.WithContext(ctx)
	var ar, br map[string]*loadRepoResult
	g.Go(func() error {
		var err error
		ar, err = p.freshLoadRepo(ctx, a)
		return err
	})
	g.Go(func() error {
		var err error
		br, err = p.freshLoadRepo(ctx, b)
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, fmt.Errorf("render error: %w", err)
	}

	if p.helmReleaseName != "" {
		for _, r := range ar {
			r.render.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
		}
		for _, r := range br {
			r.render.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
		}
	}

	for _, r := range ar {
		p.applyOutputOptions(r.render)
	}
	for _, r := range br {
		p.applyOutputOptions(r.render)
	}

	if p.isClustered() {
		leftRenders := make(map[string]*render.Render)
		for c, r := range ar {
			leftRenders[c] = r.render
		}
		rightRenders := make(map[string]*render.Render)
		for c, r := range br {
			rightRenders[c] = r.render
		}
		result, err := diff.DiffWithResultClustered(leftRenders, rightRenders, out)
		if err != nil {
			return nil, fmt.Errorf("diff error: %w", err)
		}
		var allErrors []error
		seen := make(map[string]bool)
		for _, e := range ar {
			for _, err := range e.errors {
				if !seen[err.Error()] {
					seen[err.Error()] = true
					allErrors = append(allErrors, err)
				}
			}
		}
		for _, e := range br {
			for _, err := range e.errors {
				if !seen[err.Error()] {
					seen[err.Error()] = true
					allErrors = append(allErrors, err)
				}
			}
		}
		if len(allErrors) > 0 {
			return result, &ExpansionError{Errors: allErrors}
		}
		return result, nil
	}

	result, err := diff.DiffWithResult(ar[""].render, br[""].render, out)
	if err != nil {
		return nil, fmt.Errorf("diff error: %w", err)
	}
	var allErrors []error
	seen := make(map[string]bool)
	for _, e := range append(ar[""].errors, br[""].errors...) {
		msg := e.Error()
		if !seen[msg] {
			seen[msg] = true
			allErrors = append(allErrors, e)
		}
	}
	if len(allErrors) > 0 {
		return result, &ExpansionError{Errors: allErrors}
	}
	return result, nil
}

// Opt is a functional option for configuring Preview.
type Opt func(p *Preview) error

// New creates a new Preview with the given options.
func New(opts ...Opt) (*Preview, error) {
	var p Preview
	for _, opt := range opts {
		if err := opt(&p); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// WithLogger sets the logger for the Preview.
func WithLogger(log logr.Logger) Opt {
	return func(p *Preview) error {
		p.log = log
		return nil
	}
}

// WithFilterFile configures filters from a YAML file.
func WithFilterFile(f *os.File) Opt {
	return func(p *Preview) error {
		m := &filter.FilterConfig{}
		d := yaml.NewDecoder(f)
		if err := d.Decode(m); err != nil {
			return err
		}
		p.filters = m
		return nil
	}
}

// WithFilterYAML configures filters from a raw YAML string.
func WithFilterYAML(f string) Opt {
	return func(p *Preview) error {
		m := &filter.FilterConfig{}
		if err := yaml.Unmarshal([]byte(f), m); err != nil {
			return err
		}
		p.filters = m
		return nil
	}
}

// WithFilterConfig configures filters from a parsed FilterConfig.
func WithFilterConfig(fc *filter.FilterConfig) Opt {
	return func(p *Preview) error {
		p.filters = fc
		return nil
	}
}

// WithHelm registers the Helm expander with the given settings.
func WithHelm(helmnsettings *helmcli.EnvSettings) Opt {
	return func(p *Preview) error {
		p.helmSettings = helmnsettings
		return nil
	}
}

// WithFluxKS registers the Flux Kustomization expander which discovers
// spec.path from Flux Kustomization CRs and feeds them back to the renderer.
// If a GitRepository expander is registered, it is used to resolve source paths.
func WithFluxKS() Opt {
	return func(p *Preview) error {
		p.fluxKSEnabled = true
		return nil
	}
}

// WithGitRepo registers the GitRepository expander which clones external
// repos to temp directories. Must be called before WithFluxKS.
func WithGitRepo() Opt {
	return func(p *Preview) error {
		exp, err := gitrepoexpander.NewExpander(p.log)
		if err != nil {
			return fmt.Errorf("creating git repo expander: %w", err)
		}
		p.gitRepoExpander = exp
		return nil
	}
}

func (p *Preview) expandersForSource(path string) *expander.Registry {
	if !p.fluxKSEnabled && p.gitRepoExpander == nil && p.helmSettings == nil {
		return nil
	}
	registry := expander.NewRegistry(p.log)
	var resolver *gitrepoexpander.Expander
	if p.gitRepoExpander != nil {
		resolver = p.gitRepoExpander.WithSourceRoot(path)
		registry.Register(resolver)
	}
	if p.fluxKSEnabled {
		if resolver != nil {
			registry.Register(fluxksexpander.NewExpanderWithResolver(p.log, resolver))
		} else {
			registry.Register(fluxksexpander.NewExpander(p.log))
		}
	}
	if p.helmSettings != nil {
		runner := helmexpander.NewRunner(p.helmSettings, p.log)
		registry.Register(helmexpander.NewExpander(runner, resolver, p.log))
	}
	return registry
}

// WithPaths configures the paths to render and whether to recurse into subdirectories.
// If any path contains a cluster prefix (e.g. "kube:clusters/kube"), the
// preview switches to cluster mode automatically.
func WithPaths(paths []string, recursive bool) Opt {
	return func(p *Preview) error {
		clusterPaths := make(map[string][]string)
		for _, s := range paths {
			cluster, path := config.ParseClusterPath(s)
			if cluster != "" {
				clusterPaths[cluster] = append(clusterPaths[cluster], path)
			}
		}
		if len(clusterPaths) > 0 {
			// Some paths had prefixes — merge any unprefixed paths into "" cluster
			for _, s := range paths {
				cluster, path := config.ParseClusterPath(s)
				if cluster == "" {
					clusterPaths[""] = append(clusterPaths[""], path)
				}
			}
			p.clusterPaths = clusterPaths
		} else {
			p.paths = append(p.paths, paths...)
		}
		p.recursive = recursive
		return nil
	}
}

// WithClusterPaths configures explicit per-cluster paths. This overrides any
// paths set via WithPaths.
func WithClusterPaths(clusterPaths map[string][]string) Opt {
	return func(p *Preview) error {
		p.clusterPaths = clusterPaths
		p.paths = nil
		return nil
	}
}

// applyOutputOptions applies sort and CRD filtering to the render result.
func (p *Preview) applyOutputOptions(r *render.Render) {
	if p.sortOutput {
		r.Sort()
	}
	if p.excludeCRDs {
		r.FilterCRDs()
	}
}

// WithSort enables deterministic output sorting by (kind, namespace, name).
func WithSort() Opt {
	return func(p *Preview) error {
		p.sortOutput = true
		return nil
	}
}

// WithExcludeCRDs strips CustomResourceDefinitions from rendered output.
func WithExcludeCRDs() Opt {
	return func(p *Preview) error {
		p.excludeCRDs = true
		return nil
	}
}

// WithHelmReleaseFilter filters diff output to only resources from the
// specified HelmRelease (matched by the helm.toolkit.fluxcd.io/name label).
func WithHelmReleaseFilter(name string) Opt {
	return func(p *Preview) error {
		p.helmReleaseName = name
		return nil
	}
}

// WithSOPSDecrypt enables decryption of SOPS-encrypted secrets before
// diffing or rendering. Requires access to the appropriate decryption keys.
func WithSOPSDecrypt() Opt {
	return func(p *Preview) error {
		p.sopsDecrypt = true
		return nil
	}
}

// DetectPermadiffs renders the same path twice and compares the results
// to find non-deterministic output. It generates a filter config that
// can be used to normalize these fields in subsequent diff/render runs.
// Each render pass uses a fresh set of expanders to avoid cached state.
func (p *Preview) DetectPermadiffs(ctx context.Context, path string, out io.Writer) error {
	ar, err := p.freshLoadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	if p.isClustered() {
		for cluster := range ar {
			if err := diff.WritePermadiffConfig(ar[cluster].render, br[cluster].render, out); err != nil {
				return fmt.Errorf("cluster %q: %w", cluster, err)
			}
		}
		return nil
	}
	return diff.WritePermadiffConfig(ar[""].render, br[""].render, out)
}

// GenerateInitConfig renders the repo twice to detect permadiffs and
// writes a complete .fmp.yaml config file to destPath.
func (p *Preview) GenerateInitConfig(ctx context.Context, path, destPath string) error {
	ar, err := p.freshLoadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	var renderA, renderB *render.Render
	if p.isClustered() {
		// For clustered mode, use the first cluster's render for permadiff detection
		for _, r := range ar {
			renderA = r.render
			break
		}
		for _, r := range br {
			renderB = r.render
			break
		}
	} else {
		renderA = ar[""].render
		renderB = br[""].render
	}

	diffs, err := diff.DetectPermadiffs(renderA, renderB)
	if err != nil {
		return fmt.Errorf("detecting permadiffs: %w", err)
	}

	type initConfig struct {
		Paths       []string `yaml:"paths,omitempty"`
		Recursive   *bool    `yaml:"recursive,omitempty"`
		Helm        *bool    `yaml:"helm,omitempty"`
		ResolveGit  *bool    `yaml:"resolve-git,omitempty"`
		SOPSDecrypt *bool    `yaml:"sops-decrypt,omitempty"`
		Sort        *bool    `yaml:"sort,omitempty"`
		ExcludeCRDs *bool    `yaml:"exclude-crds,omitempty"`
		Filters     []any    `yaml:"filters,omitempty"`
	}

	cfg := initConfig{
		Sort:        boolPtr(true),
		ExcludeCRDs: boolPtr(true),
	}

	if len(diffs) > 0 {
		rules := diff.GroupDiffsToRules(diffs)
		for _, rule := range rules {
			cfg.Filters = append(cfg.Filters, map[string]any{
				"kind":       "FieldNormalizer",
				"match":      rule.Match,
				"fieldPaths": rule.FieldPaths,
			})
		}
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprint(f, "# .fmp.yaml — fmp per-repo configuration\n"); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return nil
}

// freshLoadRepo creates a new set of expanders for an independent render pass.
// This is needed for permadiff detection where two separate render passes
// must not share expander state (e.g. the Helm expander's dedup map).
func (p *Preview) freshLoadRepo(ctx context.Context, path string) (map[string]*loadRepoResult, error) {
	return p.loadRepo(ctx, path)
}

func boolPtr(b bool) *bool {
	return &b
}
