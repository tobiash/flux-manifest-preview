package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"github.com/go-logr/logr"
	"helm.sh/helm/v4/pkg/action"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	helmcli "helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/getter"
	"helm.sh/helm/v4/pkg/postrenderer"
	"helm.sh/helm/v4/pkg/registry"
	ri "helm.sh/helm/v4/pkg/release"
	"helm.sh/helm/v4/pkg/repo/v1"
	"sigs.k8s.io/kustomize/api/hasher"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"

	"gopkg.in/yaml.v3"
)

// Runner handles Helm chart downloading and rendering.
type Runner struct {
	settings *helmcli.EnvSettings
	logger   logr.Logger
	storage  repo.File
	lock     sync.Mutex
	repos    sync.Map
}

type RenderTask struct {
	values          chartcommon.Values
	chart           string
	version         string
	repo            repo.Entry
	localChartPath  string
	releaseName     string
	namespace       string
	createNamespace bool
	skipCRDs        bool
	replace         bool
	disableHooks    bool
	includeCRDs     bool
	isOCI           bool
	postRenderer    postrenderer.PostRenderer
}

// NewRunner creates a new Helm runner.
func NewRunner(settings *helmcli.EnvSettings, log logr.Logger) *Runner {
	return &Runner{
		settings: settings,
		logger:   log,
	}
}

// RenderCharts renders multiple charts in parallel.
// Charts that fail to render are collected as errors and skipped; partial results are returned.
func (r *Runner) RenderCharts(ctx context.Context, releases []RenderTask) (resmap.ResMap, []error, error) {
	res := resmap.New()

	type result struct {
		resources resmap.ResMap
		err       error
		task      RenderTask
	}
	results := make([]result, len(releases))

	var wg sync.WaitGroup
	for i, h := range releases {
		i := i
		h := h
		wg.Add(1)
		go func() {
			defer wg.Done()
			chartRes, err := r.renderChart(ctx, &h)
			results[i] = result{resources: chartRes, err: err, task: h}
		}()
	}
	wg.Wait()

	var errs []error
	for _, chartResult := range results {
		if chartResult.err != nil {
			r.logger.V(1).Info("skipping chart render", "chart", chartResult.task.chart, "namespace", chartResult.task.namespace, "error", chartResult.err)
			errs = append(errs, fmt.Errorf("HelmRelease %s/%s: %w", chartResult.task.namespace, chartResult.task.chart, chartResult.err))
			continue
		}
		for _, res := range chartResult.resources.Resources() {
			labels := res.GetLabels()
			if labels == nil {
				labels = make(map[string]string)
			}
			labels["helm.toolkit.fluxcd.io/name"] = chartResult.task.releaseName
			labels["helm.toolkit.fluxcd.io/namespace"] = chartResult.task.namespace
			res.SetLabels(labels)
		}

		if err := absorbResMap(res, chartResult.resources, r.logger); err != nil {
			return nil, errs, fmt.Errorf("absorbing resources for %s/%s: %w", chartResult.task.releaseName, chartResult.task.namespace, err)
		}
	}
	return res, errs, nil
}

func (r *Runner) renderChart(ctx context.Context, t *RenderTask) (resmap.ResMap, error) {
	cfg := new(action.Configuration)
	cfg.Init(r.settings.RESTClientGetter(), t.namespace, os.Getenv("HELM_DRIVER"))

	install := action.NewInstall(cfg)
	install.DryRunStrategy = action.DryRunClient
	install.Namespace = t.namespace
	install.CreateNamespace = t.createNamespace
	install.ReleaseName = t.releaseName
	install.SkipCRDs = t.skipCRDs
	install.Replace = t.replace
	install.DisableHooks = t.disableHooks
	install.IncludeCRDs = t.includeCRDs

	var (
		chartRef string
		chart    *chart.Chart
	)
	if t.localChartPath != "" {
		loaded, err := loader.Load(t.localChartPath)
		if err != nil {
			return nil, fmt.Errorf("loading local chart %s: %w", t.localChartPath, err)
		}
		chart = loaded
		r.logger.V(1).Info("loaded local chart", "chart", t.chart, "path", t.localChartPath)
	} else if t.isOCI {
		// For OCI charts, construct the full reference, skip repo index, and set up a registry client.
		chartRef = strings.TrimSuffix(t.repo.URL, "/") + "/" + t.chart
		install.ChartPathOptions.Version = t.version
		regClient, err := registry.NewClient()
		if err != nil {
			return nil, fmt.Errorf("creating registry client: %w", err)
		}
		install.SetRegistryClient(regClient)
	} else {
		if err := r.getAndUpdateRepo(&t.repo); err != nil {
			return nil, err
		}
		install.ChartPathOptions.Version = t.version
		install.ChartPathOptions.RepoURL = t.repo.URL
		install.ChartPathOptions.Username = t.repo.Username
		install.ChartPathOptions.Password = t.repo.Password
		install.ChartPathOptions.CaFile = t.repo.CAFile
		install.ChartPathOptions.CertFile = t.repo.CertFile
		install.ChartPathOptions.InsecureSkipTLSVerify = t.repo.InsecureSkipTLSVerify
		install.ChartPathOptions.PassCredentialsAll = t.repo.PassCredentialsAll
		install.ChartPathOptions.KeyFile = t.repo.KeyFile
		chartRef = t.chart
	}
	if chart == nil {
		cp, err := install.ChartPathOptions.LocateChart(chartRef, r.settings)
		if err != nil {
			return nil, fmt.Errorf("error locating chart: %w", err)
		}
		r.logger.V(1).Info("loaded chart from repo", "chart", t.chart, "repo", t.repo.Name, "url", t.repo.URL, "path", cp)
		loaded, err := loader.Load(cp)
		if err != nil {
			return nil, err
		}
		chart = loaded
	}
	rel, err := install.RunWithContext(ctx, chart, t.values)
	if err != nil {
		return nil, err
	}

	var manifests bytes.Buffer
	if rel != nil {
		acc, err := ri.NewAccessor(rel)
		if err != nil {
			return nil, fmt.Errorf("unexpected release type: %w", err)
		}
		fmt.Fprintln(&manifests, strings.TrimSpace(acc.Manifest()))
		if !install.DisableHooks {
			for _, h := range acc.Hooks() {
				ha, err := ri.NewHookAccessor(h)
				if err != nil {
					continue
				}
				fmt.Fprintf(&manifests, "---\n# Source: %s\n%s\n", ha.Path(), ha.Manifest())
			}
		}
	}
	renderedManifests, err := runPostRenderer(t.postRenderer, &manifests)
	if err != nil {
		return nil, fmt.Errorf("running post renderer: %w", err)
	}
	manifests = *renderedManifests
	return parseManifests(manifests.Bytes(), r.logger)
}

func runPostRenderer(renderer postrenderer.PostRenderer, manifests *bytes.Buffer) (*bytes.Buffer, error) {
	if renderer == nil {
		return manifests, nil
	}
	return renderer.Run(manifests)
}

// parseManifests splits a multi-document YAML byte slice into individual
// resources, parsing each one independently. Resources that fail to parse
// (e.g. missing metadata.name) are skipped with a log warning.
func parseManifests(data []byte, log logr.Logger) (resmap.ResMap, error) {
	result := resmap.New()
	factory := resmap.NewFactory(resource.NewFactory(&hasher.Hasher{}))

	// kustomize's NewResMapFromBytes rejects any document missing metadata.name.
	// Split on document boundaries and parse individually so one bad resource
	// doesn't sink the entire chart output.
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		var doc any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decoding manifest: %w", err)
		}
		if doc == nil {
			continue
		}
		if err := enc.Encode(doc); err != nil {
			return nil, fmt.Errorf("encoding manifest: %w", err)
		}
		enc.Close()
		if buf.Len() == 0 {
			continue
		}

		rm, err := factory.NewResMapFromBytes(buf.Bytes())
		if err != nil {
			log.V(1).Info("skipping malformed resource", "error", err)
			continue
		}
		if err := absorbResMap(result, rm, log); err != nil {
			return nil, fmt.Errorf("absorbing resources: %w", err)
		}
	}
	return result, nil
}

// absorbResMap merges src into dst, replacing duplicates instead of failing.
func absorbResMap(dst, src resmap.ResMap, log logr.Logger) error {
	for _, r := range src.Resources() {
		id := r.CurId()
		if existing, err := dst.GetById(id); err == nil {
			// Duplicate: remove old and add new.
			dst.Remove(existing.CurId())
			log.V(1).Info("replacing duplicate resource", "id", id)
		}
		if err := dst.Append(r); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runner) getAndUpdateRepo(entry *repo.Entry) error {
	_, ok := r.repos.Load(entry.URL)
	if ok {
		return nil
	}

	r.lock.Lock()
	defer r.lock.Unlock()
	_, ok = r.repos.Load(entry.URL)
	if ok {
		return nil
	}

	chartRepo, err := repo.NewChartRepository(entry, getter.All(r.settings))
	if err != nil {
		return err
	}
	chartRepo.CachePath = r.settings.RepositoryCache
	_, err = chartRepo.DownloadIndexFile()
	if err != nil {
		return err
	}
	if r.storage.Has(entry.Name) {
		return nil
	}
	r.storage.Update(entry)
	err = r.storage.WriteFile(r.settings.RepositoryConfig, 0o644)
	if err != nil {
		return err
	}
	r.repos.Store(entry.URL, true)
	return nil
}

// ResolveVersion resolves a potentially semver-range version string to a
// concrete version by loading the repo index and finding the latest match.
// If the version is already concrete (not a range), it is returned as-is.
func (r *Runner) ResolveVersion(repoURL, chart, version string) (string, error) {
	// If it looks like a plain version, return it directly.
	if isPlainVersion(version) {
		return version, nil
	}

	// Load the cached index file.
	idxFile := r.settings.RepositoryCache + "/" + fmt.Sprintf("%x.index.yaml", sha256hex(repoURL))
	data, err := os.ReadFile(idxFile)
	if err != nil {
		return version, nil // fallback to the raw version string
	}

	var idx repo.IndexFile
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return version, nil
	}

	chartVersions, ok := idx.Entries[chart]
	if !ok || len(chartVersions) == 0 {
		return version, nil
	}

	constraint, err := semver.NewConstraint(version)
	if err != nil {
		return version, nil // not a valid constraint, use as-is
	}

	for _, cv := range chartVersions {
		v, err := semver.NewVersion(cv.Version)
		if err != nil {
			continue
		}
		if constraint.Check(v) {
			return v.String(), nil
		}
	}

	return "", fmt.Errorf("no version of %s matches constraint %s", chart, version)
}

// isPlainVersion returns true if the version looks like a concrete semver
// rather than a range constraint. Ranges typically start with operators
// like >=, <=, >, <, ^, ~, or contain || for unions.
func isPlainVersion(version string) bool {
	if version == "" || version == "*" || version == "latest" {
		return true
	}
	// If it parses as a strict semver, it's plain.
	if _, err := semver.StrictNewVersion(version); err == nil {
		return true
	}
	return false
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
