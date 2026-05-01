package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	"github.com/tobiash/flux-manifest-preview/pkg/sops"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
	helmcli "helm.sh/helm/v4/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/filesys"
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

// loadRepoResult holds the result of loading and expanding a repo.
type loadRepoResult struct {
	render   *render.Render
	errors   []error
	warnings []error
}

func (p *Preview) loadRepo(ctx context.Context, path string) (map[string]*loadRepoResult, error) {
	if p.isClustered() {
		return p.loadRepoClustered(ctx, path)
	}
	result, err := p.loadRepoFlat(ctx, path)
	if err != nil {
		return nil, err
	}
	return map[string]*loadRepoResult{"": result}, nil
}

func (p *Preview) loadRepoFlat(ctx context.Context, path string) (*loadRepoResult, error) {
	log := p.log.WithValues("renderPath", path)
	r := render.NewDefaultRender(log)
	fSys := filesys.MakeFsOnDisk()
	var collectedErrors []error
	activeExpanders := p.expandersForSource(path)

	// Seed the queue with user-specified paths.
	queue := make([]expander.DiscoveredPath, len(p.paths))
	for i, p := range p.paths {
		queue[i] = expander.DiscoveredPath{Path: p, Producer: fmt.Sprintf("path %s", p)}
	}

	// userPaths tracks which paths were explicitly requested by the user.
	// Missing user paths are errors; missing discovered paths are skipped.
	userPaths := make(map[string]bool, len(p.paths))
	for _, p := range p.paths {
		userPaths[p] = true
	}

	// visited tracks paths already rendered to prevent cycles.
	visited := make(map[string]bool)

	const maxIterations = 100
	for iteration := 0; len(queue) > 0; iteration++ {
		if iteration > maxIterations {
			return nil, fmt.Errorf("expansion loop exceeded %d iterations, possible cycle", maxIterations)
		}

		// Render all newly discovered paths.
		for _, dp := range queue {
			baseDir := dp.BaseDir
			if baseDir == "" {
				baseDir = path
			}
			full := filepath.Join(baseDir, dp.Path)
			if visited[full] {
				continue
			}
			visited[full] = true

			if !fSys.Exists(full) {
				if userPaths[dp.Path] {
					return nil, fmt.Errorf("path %q does not exist", dp.Path)
				}
				log.V(1).Info("skipping non-existent path", "path", dp.Path)
				continue
			}

			log.V(1).Info("rendering path", "path", dp.Path, "baseDir", dp.BaseDir)
			count := r.Size()
			producer := dp.Producer
			if producer == "" {
				producer = fmt.Sprintf("path %s", dp.Path)
			}
			if p.recursive {
				if err := r.AddPathsWithProducer(fSys, full, producer); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			} else {
				if err := r.AddPathWithProducer(fSys, full, producer); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			}
			if dp.Namespace != "" {
				r.ApplyNamespaceToNew(count, dp.Namespace)
			}
			r.MarkProvenanceToNew(count, producer)
		}

		// Run expanders to discover new paths and expand resources.
		queue = nil
		if activeExpanders != nil {
			result, err := activeExpanders.Expand(ctx, r)
			if err != nil {
				return nil, fmt.Errorf("failed to expand: %w", err)
			}
			collectedErrors = append(collectedErrors, result.Errors...)
			if result.Resources != nil {
				if err := r.AbsorbAll(result.Resources); err != nil {
					return nil, fmt.Errorf("failed to absorb expanded resources: %w", err)
				}
			}
			// Only queue paths we haven't rendered yet.
			for _, dp := range result.DiscoveredPaths {
				baseDir := dp.BaseDir
				if baseDir == "" {
					baseDir = path
				}
				full := filepath.Join(baseDir, dp.Path)
				if !visited[full] {
					queue = append(queue, dp)
				}
			}
		}
	}

	if p.filters != nil {
		for _, f := range p.filters.Filters {
			if err := r.ApplyFilter(f.Filter); err != nil {
				return nil, err
			}
		}
	}

	if p.sopsDecrypt {
		if err := sops.DecryptResources(r); err != nil {
			return nil, fmt.Errorf("sops decryption failed: %w", err)
		}
	}
	warnings := r.Warnings()

	return &loadRepoResult{render: r, errors: collectedErrors, warnings: warnings}, nil
}

func (p *Preview) loadRepoClustered(ctx context.Context, path string) (map[string]*loadRepoResult, error) {
	results := make(map[string]*loadRepoResult, len(p.clusterPaths))
	for cluster, paths := range p.clusterPaths {
		log := p.log.WithValues("cluster", cluster, "renderPath", path)
		r := render.NewDefaultRender(log)
		fSys := filesys.MakeFsOnDisk()
		var collectedErrors []error
		activeExpanders := p.expandersForSource(path)

		queue := make([]expander.DiscoveredPath, len(paths))
		for i, p := range paths {
			queue[i] = expander.DiscoveredPath{Path: p, Producer: fmt.Sprintf("path %s", p)}
		}

		userPaths := make(map[string]bool, len(paths))
		for _, p := range paths {
			userPaths[p] = true
		}

		visited := make(map[string]bool)

		const maxIterations = 100
		for iteration := 0; len(queue) > 0; iteration++ {
			if iteration > maxIterations {
				return nil, fmt.Errorf("cluster %q: expansion loop exceeded %d iterations, possible cycle", cluster, maxIterations)
			}

			for _, dp := range queue {
				baseDir := dp.BaseDir
				if baseDir == "" {
					baseDir = path
				}
				full := filepath.Join(baseDir, dp.Path)
				if visited[full] {
					continue
				}
				visited[full] = true

				if !fSys.Exists(full) {
					if userPaths[dp.Path] {
						return nil, fmt.Errorf("cluster %q: path %q does not exist", cluster, dp.Path)
					}
					log.V(1).Info("skipping non-existent path", "path", dp.Path)
					continue
				}

				log.V(1).Info("rendering path", "path", dp.Path, "baseDir", dp.BaseDir)
				count := r.Size()
				producer := dp.Producer
				if producer == "" {
					producer = fmt.Sprintf("path %s", dp.Path)
				}
				if p.recursive {
					if err := r.AddPathsWithProducer(fSys, full, producer); err != nil {
						return nil, fmt.Errorf("cluster %q: failed to add path %s: %w", cluster, full, err)
					}
				} else {
					if err := r.AddPathWithProducer(fSys, full, producer); err != nil {
						return nil, fmt.Errorf("cluster %q: failed to add path %s: %w", cluster, full, err)
					}
				}
				if dp.Namespace != "" {
					r.ApplyNamespaceToNew(count, dp.Namespace)
				}
				r.MarkProvenanceToNew(count, producer)
			}

			queue = nil
			if activeExpanders != nil {
				result, err := activeExpanders.Expand(ctx, r)
				if err != nil {
					return nil, fmt.Errorf("cluster %q: failed to expand: %w", cluster, err)
				}
				collectedErrors = append(collectedErrors, result.Errors...)
				if result.Resources != nil {
					if err := r.AbsorbAll(result.Resources); err != nil {
						return nil, fmt.Errorf("cluster %q: failed to absorb expanded resources: %w", cluster, err)
					}
				}
				for _, dp := range result.DiscoveredPaths {
					baseDir := dp.BaseDir
					if baseDir == "" {
						baseDir = path
					}
					full := filepath.Join(baseDir, dp.Path)
					if !visited[full] {
						queue = append(queue, dp)
					}
				}
			}
		}

		if p.filters != nil {
			for _, f := range p.filters.Filters {
				if err := r.ApplyFilter(f.Filter); err != nil {
					return nil, err
				}
			}
		}

		if p.sopsDecrypt {
			if err := sops.DecryptResources(r); err != nil {
				return nil, fmt.Errorf("cluster %q: sops decryption failed: %w", cluster, err)
			}
		}
		warnings := r.Warnings()
		results[cluster] = &loadRepoResult{render: r, errors: collectedErrors, warnings: warnings}
	}
	return results, nil
}

// Render renders the resources at path and writes the YAML output.
func (p *Preview) Render(ctx context.Context, path string, out io.Writer) error {
	results, err := p.loadRepo(ctx, path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	if p.isClustered() {
		_, _ = fmt.Fprintln(out, "# Rendered manifests")
		for cluster, result := range results {
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
		for _, result := range results {
			allErrors = append(allErrors, result.errors...)
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
		for cluster, result := range results {
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
		for _, result := range results {
			allErrors = append(allErrors, result.errors...)
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
	for cluster, result := range results {
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
	for _, result := range results {
		for _, e := range result.errors {
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

// freshLoadRepo creates a fresh Preview instance with the same configuration
// but new expanders, then loads the repo. This is needed for permadiff
// detection where we need independent render passes that don't share
// cached expander state (e.g. the Helm expander's expanded-release map).
func (p *Preview) freshLoadRepo(ctx context.Context, path string) (map[string]*loadRepoResult, error) {
	fresh := &Preview{
		paths:           p.paths,
		clusterPaths:    p.clusterPaths,
		recursive:       p.recursive,
		sortOutput:      p.sortOutput,
		excludeCRDs:     p.excludeCRDs,
		sopsDecrypt:     p.sopsDecrypt,
		filters:         p.filters,
		fluxKSEnabled:   p.fluxKSEnabled,
		log:             p.log,
		gitRepoExpander: p.gitRepoExpander,
		helmSettings:    p.helmSettings,
	}

	return fresh.loadRepo(ctx, path)
}

func boolPtr(b bool) *bool {
	return &b
}
