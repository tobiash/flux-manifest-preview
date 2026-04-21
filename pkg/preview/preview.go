package preview

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
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
	ctx             context.Context
}

// ExpansionError is returned when one or more non-fatal errors were
// encountered during expansion (e.g. a HelmRelease whose chart could
// not be resolved). The render/diff output is still produced but may
// be incomplete.
type ExpansionError struct {
	Errors []error
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
	render *render.Render
	errors []error
}

func (p *Preview) loadRepo(path string) (*loadRepoResult, error) {
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
			result, err := activeExpanders.Expand(p.ctx, r)
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
	collectedErrors = append(collectedErrors, r.Warnings()...)

	return &loadRepoResult{render: r, errors: collectedErrors}, nil
}

// Render renders the resources at path and writes the YAML output.
func (p *Preview) Render(path string, out io.Writer) error {
	result, err := p.loadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	p.applyOutputOptions(result.render)
	yaml, err := result.render.AsYaml()
	if err != nil {
		return fmt.Errorf("error transforming to yaml: %w", err)
	}
	if _, err := out.Write(yaml); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	if len(result.errors) > 0 {
		return &ExpansionError{Errors: result.errors}
	}
	return nil
}

// RenderJSON renders the resources at path and writes JSON output.
func (p *Preview) RenderJSON(path string, out io.Writer) error {
	result, err := p.loadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	p.applyOutputOptions(result.render)
	json, err := result.render.AsJSON()
	if err != nil {
		return fmt.Errorf("error transforming to json: %w", err)
	}
	if _, err := out.Write(json); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	if len(result.errors) > 0 {
		return &ExpansionError{Errors: result.errors}
	}
	return nil
}

// Test validates that all Kustomizations build and HelmReleases render.
// Returns nil on success, or an error describing the failure.
func (p *Preview) Test(path string, out io.Writer) error {
	result, err := p.loadRepo(path)
	if err != nil {
		fmt.Fprintf(out, "FAIL: %v\n", err)
		return err
	}
	if len(result.errors) > 0 {
		for _, e := range result.errors {
			fmt.Fprintf(out, "WARN: %v\n", e)
		}
		fmt.Fprintln(out, "PASS (with warnings)")
		return nil
	}
	fmt.Fprintln(out, "PASS")
	return nil
}

// Diff computes and writes the diff between two repository paths.
// If a HelmRelease filter is set, only resources from that release are included.
func (p *Preview) Diff(a, b string, out io.Writer) error {
	g, _ := errgroup.WithContext(p.ctx)
	var ar, br *loadRepoResult
	g.Go(func() error {
		var err error
		ar, err = p.freshLoadRepo(a)
		return err
	})
	g.Go(func() error {
		var err error
		br, err = p.freshLoadRepo(b)
		return err
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
	if p.ctx == nil {
		p.ctx = context.Background()
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
func WithHelm(helmsettings *helmcli.EnvSettings) Opt {
	return func(p *Preview) error {
		p.helmSettings = helmsettings
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
func WithPaths(paths []string, recursive bool) Opt {
	return func(p *Preview) error {
		p.paths = append(p.paths, paths...)
		p.recursive = recursive
		return nil
	}
}

// WithContext sets the context for the Preview.
func WithContext(ctx context.Context) Opt {
	return func(p *Preview) error {
		p.ctx = ctx
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
// diffing or rendering. Requires the sops binary in PATH and access
// to the appropriate decryption keys.
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
func (p *Preview) DetectPermadiffs(path string, out io.Writer) error {
	ar, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	return diff.WritePermadiffConfig(ar.render, br.render, out)
}

// GenerateInitConfig renders the repo twice to detect permadiffs and
// writes a complete .fmp.yaml config file to destPath.
func (p *Preview) GenerateInitConfig(path, destPath string) error {
	ar, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	diffs, err := diff.DetectPermadiffs(ar.render, br.render)
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
	defer f.Close()

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
func (p *Preview) freshLoadRepo(path string) (*loadRepoResult, error) {
	fresh := &Preview{
		paths:           p.paths,
		recursive:       p.recursive,
		sortOutput:      p.sortOutput,
		excludeCRDs:     p.excludeCRDs,
		sopsDecrypt:     p.sopsDecrypt,
		filters:         p.filters,
		fluxKSEnabled:   p.fluxKSEnabled,
		log:             p.log,
		ctx:             p.ctx,
		gitRepoExpander: p.gitRepoExpander,
		helmSettings:    p.helmSettings,
	}

	return fresh.loadRepo(path)
}

func boolPtr(b bool) *bool {
	return &b
}
