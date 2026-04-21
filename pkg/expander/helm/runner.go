package helm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"helm.sh/helm/v4/pkg/action"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	chart "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/chart/v2/loader"
	helmcli "helm.sh/helm/v4/pkg/cli"
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
			_ = res.SetLabels(labels)
		}

		if err := absorbResMap(res, chartResult.resources, r.logger); err != nil {
			return nil, errs, fmt.Errorf("absorbing resources for %s/%s: %w", chartResult.task.releaseName, chartResult.task.namespace, err)
		}
	}
	return res, errs, nil
}

func (r *Runner) renderChart(ctx context.Context, t *RenderTask) (resmap.ResMap, error) {
	cfg := new(action.Configuration)
	if err := cfg.Init(r.settings.RESTClientGetter(), t.namespace, os.Getenv("HELM_DRIVER")); err != nil {
		return nil, fmt.Errorf("initializing helm configuration: %w", err)
	}

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
		install.Version = t.version
		regClient, err := registry.NewClient()
		if err != nil {
			return nil, fmt.Errorf("creating registry client: %w", err)
		}
		install.SetRegistryClient(regClient)
	} else {
		install.Version = t.version
		install.RepoURL = t.repo.URL
		install.Username = t.repo.Username
		install.Password = t.repo.Password
		install.CaFile = t.repo.CAFile
		install.CertFile = t.repo.CertFile
		install.InsecureSkipTLSVerify = t.repo.InsecureSkipTLSVerify
		install.PassCredentialsAll = t.repo.PassCredentialsAll
		install.KeyFile = t.repo.KeyFile
		chartRef = t.chart
	}
	if chart == nil {
		cp, err := install.LocateChart(chartRef, r.settings)
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
		if err := enc.Close(); err != nil {
			return nil, fmt.Errorf("closing encoder: %w", err)
		}
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
			_ = dst.Remove(existing.CurId())
			log.V(1).Info("replacing duplicate resource", "id", id)
		}
		if err := dst.Append(r); err != nil {
			return err
		}
	}
	return nil
}
