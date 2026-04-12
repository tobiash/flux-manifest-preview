package preview

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	fluxksexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/fluxks"
	helmexpander "github.com/tobiash/flux-manifest-preview/pkg/expander/helm"
	"github.com/tobiash/flux-manifest-preview/pkg/filter"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
	helmcli "helm.sh/helm/v4/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Preview renders and diffs Flux GitOps resources.
type Preview struct {
	paths       []string
	recursive   bool
	sortOutput  bool
	excludeCRDs bool
	filters     *filter.FilterConfig
	expanders   *expander.Registry
	log         logr.Logger
	ctx         context.Context
}

func (p *Preview) loadRepo(path string) (*render.Render, error) {
	log := p.log.WithValues("renderPath", path)
	r := render.NewDefaultRender(log)
	fSys := filesys.MakeFsOnDisk()

	// Seed the queue with user-specified paths.
	queue := make([]string, len(p.paths))
	copy(queue, p.paths)

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
		for _, relPath := range queue {
			full := filepath.Join(path, relPath)
			if visited[full] {
				continue
			}
			visited[full] = true

			if !fSys.Exists(full) {
				if userPaths[relPath] {
					return nil, fmt.Errorf("path %q does not exist", relPath)
				}
				log.Info("skipping non-existent path", "path", relPath)
				continue
			}

			log.Info("rendering path", "path", relPath)
			if p.recursive {
				if err := r.AddPaths(fSys, full); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			} else {
				if err := r.AddPath(fSys, full); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			}
		}

		// Run expanders to discover new paths and expand resources.
		queue = nil
		if p.expanders != nil {
			result, err := p.expanders.Expand(p.ctx, r)
			if err != nil {
				return nil, fmt.Errorf("failed to expand: %w", err)
			}
			if result.Resources != nil {
				if err := r.AppendAll(result.Resources); err != nil {
					return nil, fmt.Errorf("failed to absorb expanded resources: %w", err)
				}
			}
			// Only queue paths we haven't rendered yet.
			for _, discoveredPath := range result.DiscoveredPaths {
				full := filepath.Join(path, discoveredPath)
				if !visited[full] {
					queue = append(queue, discoveredPath)
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

	return r, nil
}

// Render renders the resources at path and writes the YAML output.
func (p *Preview) Render(path string, out io.Writer) error {
	r, err := p.loadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	p.applyOutputOptions(r)
	yaml, err := r.AsYaml()
	if err != nil {
		return fmt.Errorf("error transforming to yaml: %w", err)
	}
	_, err = out.Write(yaml)
	if err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	return nil
}

// Test validates that all Kustomizations build and HelmReleases render.
// Returns nil on success, or an error describing the failure.
func (p *Preview) Test(path string, out io.Writer) error {
	_, err := p.loadRepo(path)
	if err != nil {
		fmt.Fprintf(out, "FAIL: %v\n", err)
		return err
	}
	fmt.Fprintln(out, "PASS")
	return nil
}

func (p *Preview) renderFn(repo string, out **render.Render) func() error {
	return func() error {
		var err error
		*out, err = p.loadRepo(repo)
		return err
	}
}

// Diff computes and writes the diff between two repository paths.
func (p *Preview) Diff(a, b string, out io.Writer) error {
	g, _ := errgroup.WithContext(p.ctx)
	var ar, br *render.Render
	g.Go(p.renderFn(a, &ar))
	g.Go(p.renderFn(b, &br))
	if err := g.Wait(); err != nil {
		return fmt.Errorf("render error: %w", err)
	}
	p.applyOutputOptions(ar)
	p.applyOutputOptions(br)
	if err := diff.Diff(ar, br, out); err != nil {
		return fmt.Errorf("diff error: %w", err)
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

// WithHelm registers the Helm expander with the given settings.
func WithHelm(helmsettings *helmcli.EnvSettings) Opt {
	return func(p *Preview) error {
		p.ensureRegistry()
		runner := helmexpander.NewRunner(helmsettings, p.log)
		p.expanders.Register(helmexpander.NewExpander(runner, p.log))
		return nil
	}
}

// WithFluxKS registers the Flux Kustomization expander which discovers
// spec.path from Flux Kustomization CRs and feeds them back to the renderer.
func WithFluxKS() Opt {
	return func(p *Preview) error {
		p.ensureRegistry()
		p.expanders.Register(fluxksexpander.NewExpander(p.log))
		return nil
	}
}

// ensureRegistry lazily initializes the expander registry.
func (p *Preview) ensureRegistry() {
	if p.expanders == nil {
		p.expanders = expander.NewRegistry(p.log)
	}
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