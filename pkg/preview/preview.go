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

type Preview struct {
	paths           []string
	recursive       bool
	sortOutput      bool
	excludeCRDs     bool
	helmReleaseName string
	sopsDecrypt     bool
	filters         *filter.FilterConfig
	expanders       *expander.Registry
	gitRepoExpander *gitrepoexpander.Expander
	helmSettings    *helmcli.EnvSettings
	log             logr.Logger
	ctx             context.Context
}

func (p *Preview) loadRepo(path string) (*render.Render, error) {
	log := p.log.WithValues("renderPath", path)
	r := render.NewDefaultRender(log)
	fSys := filesys.MakeFsOnDisk()

	queue := make([]expander.DiscoveredPath, len(p.paths))
	for i, p := range p.paths {
		queue[i] = expander.DiscoveredPath{Path: p}
	}

	userPaths := make(map[string]bool, len(p.paths))
	for _, p := range p.paths {
		userPaths[p] = true
	}

	visited := make(map[string]bool)

	const maxIterations = 100
	for iteration := 0; len(queue) > 0; iteration++ {
		if iteration > maxIterations {
			return nil, fmt.Errorf("expansion loop exceeded %d iterations, possible cycle", maxIterations)
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
					return nil, fmt.Errorf("path %q does not exist", dp.Path)
				}
				log.Info("skipping non-existent path", "path", dp.Path)
				continue
			}

			log.Info("rendering path", "path", dp.Path, "baseDir", dp.BaseDir)
			count := r.Size()
			if p.recursive {
				if err := r.AddPaths(fSys, full); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			} else {
				if err := r.AddPath(fSys, full); err != nil {
					return nil, fmt.Errorf("failed to add path %s: %w", full, err)
				}
			}
			if dp.Namespace != "" {
				r.ApplyNamespaceToNew(count, dp.Namespace)
			}
		}

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

	return r, nil
}

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
	if _, err := out.Write(yaml); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	return nil
}

func (p *Preview) RenderJSON(path string, out io.Writer) error {
	r, err := p.loadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo: %w", err)
	}
	p.applyOutputOptions(r)
	json, err := r.AsJSON()
	if err != nil {
		return fmt.Errorf("error transforming to json: %w", err)
	}
	if _, err := out.Write(json); err != nil {
		return fmt.Errorf("error writing output: %w", err)
	}
	return nil
}

func (p *Preview) Test(path string, out io.Writer) error {
	_, err := p.loadRepo(path)
	if err != nil {
		fmt.Fprintf(out, "FAIL: %v\n", err)
		return err
	}
	fmt.Fprintln(out, "PASS")
	return nil
}

func (p *Preview) Diff(a, b string, out io.Writer) error {
	g, _ := errgroup.WithContext(p.ctx)
	var ar, br *render.Render
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
		ar.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
		br.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
	}

	p.applyOutputOptions(ar)
	p.applyOutputOptions(br)
	if err := diff.Diff(ar, br, out); err != nil {
		return fmt.Errorf("diff error: %w", err)
	}
	return nil
}

type Opt func(p *Preview) error

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

func WithLogger(log logr.Logger) Opt {
	return func(p *Preview) error {
		p.log = log
		return nil
	}
}

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

func WithFilterConfig(fc *filter.FilterConfig) Opt {
	return func(p *Preview) error {
		p.filters = fc
		return nil
	}
}

func WithHelm(helmsettings *helmcli.EnvSettings) Opt {
	return func(p *Preview) error {
		p.helmSettings = helmsettings
		p.ensureRegistry()
		runner := helmexpander.NewRunner(helmsettings, p.log)
		p.expanders.Register(helmexpander.NewExpander(runner, p.log))
		return nil
	}
}

func WithFluxKS() Opt {
	return func(p *Preview) error {
		p.ensureRegistry()
		if p.gitRepoExpander != nil {
			p.expanders.Register(fluxksexpander.NewExpanderWithResolver(p.log, p.gitRepoExpander))
		} else {
			p.expanders.Register(fluxksexpander.NewExpander(p.log))
		}
		return nil
	}
}

func WithGitRepo() Opt {
	return func(p *Preview) error {
		p.ensureRegistry()
		exp, err := gitrepoexpander.NewExpander(p.log)
		if err != nil {
			return fmt.Errorf("creating git repo expander: %w", err)
		}
		p.gitRepoExpander = exp
		p.expanders.Register(exp)
		return nil
	}
}

func (p *Preview) ensureRegistry() {
	if p.expanders == nil {
		p.expanders = expander.NewRegistry(p.log)
	}
}

func WithPaths(paths []string, recursive bool) Opt {
	return func(p *Preview) error {
		p.paths = append(p.paths, paths...)
		p.recursive = recursive
		return nil
	}
}

func WithContext(ctx context.Context) Opt {
	return func(p *Preview) error {
		p.ctx = ctx
		return nil
	}
}

func (p *Preview) applyOutputOptions(r *render.Render) {
	if p.sortOutput {
		r.Sort()
	}
	if p.excludeCRDs {
		r.FilterCRDs()
	}
}

func WithSort() Opt {
	return func(p *Preview) error {
		p.sortOutput = true
		return nil
	}
}

func WithExcludeCRDs() Opt {
	return func(p *Preview) error {
		p.excludeCRDs = true
		return nil
	}
}

func WithHelmReleaseFilter(name string) Opt {
	return func(p *Preview) error {
		p.helmReleaseName = name
		return nil
	}
}

func WithSOPSDecrypt() Opt {
	return func(p *Preview) error {
		p.sopsDecrypt = true
		return nil
	}
}

func (p *Preview) DetectPermadiffs(path string, out io.Writer) error {
	ar, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	return diff.WritePermadiffConfig(ar, br, out)
}

func (p *Preview) GenerateInitConfig(path, destPath string) error {
	ar, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (first pass): %w", err)
	}

	br, err := p.freshLoadRepo(path)
	if err != nil {
		return fmt.Errorf("error loading repo (second pass): %w", err)
	}

	diffs, err := diff.DetectPermadiffs(ar, br)
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

func (p *Preview) freshLoadRepo(path string) (*render.Render, error) {
	fresh := &Preview{
		paths:       p.paths,
		recursive:   p.recursive,
		sortOutput:  p.sortOutput,
		excludeCRDs: p.excludeCRDs,
		sopsDecrypt: p.sopsDecrypt,
		filters:     p.filters,
		log:         p.log,
		ctx:         p.ctx,
	}

	fresh.ensureRegistry()
	if p.gitRepoExpander != nil {
		fresh.expanders.Register(p.gitRepoExpander)
	}
	fresh.expanders.Register(fluxksexpander.NewExpander(p.log))
	if p.helmSettings != nil {
		runner := helmexpander.NewRunner(p.helmSettings, p.log)
		fresh.expanders.Register(helmexpander.NewExpander(runner, p.log))
	}

	return fresh.loadRepo(path)
}

func boolPtr(b bool) *bool {
	return &b
}
