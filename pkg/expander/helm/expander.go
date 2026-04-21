package helm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxchartutil "github.com/fluxcd/pkg/chartutil"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/repo/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

var (
	helmReleaseGVK = resid.NewGvk("helm.toolkit.fluxcd.io", "v2", "HelmRelease")
	helmRepoGVK    = resid.NewGvk("source.toolkit.fluxcd.io", "v1", "HelmRepository")
	ociRepoGVK     = resid.NewGvk("source.toolkit.fluxcd.io", "v1", "OCIRepository")
	secretGVK      = resid.NewGvk("", "v1", "Secret")
	configMapGVK   = resid.NewGvk("", "v1", "ConfigMap")
)

// matchGVK reports true if the resource's GVK matches the target by group and kind.
// Version is ignored because Flux resources exist in multiple API versions,
// but the fields we read are compatible enough to decode into the current
// public API structs directly.
func matchGVK(resGvk resid.Gvk, target resid.Gvk) bool {
	return resGvk.Group == target.Group && resGvk.Kind == target.Kind
}

type chartRunner interface {
	RenderCharts(ctx context.Context, releases []RenderTask) (resmap.ResMap, []error, error)
}

type chartSourceResolver interface {
	ResolvePath(namespace, name string) (string, bool)
}

// Expander implements the expander.Expander interface for Helm.
// It is safe for concurrent use -- each call to Expand operates on isolated state.
// The Expander tracks which releases have already been expanded to avoid
// duplicating resources across iterative expansion loops.
type Expander struct {
	runner   chartRunner
	resolver chartSourceResolver
	logger   logr.Logger
	scheme   *runtime.Scheme
	expanded map[string]bool // "namespace/name" of already-expanded releases
}

// expandState holds per-invocation state for a single Expand call.
type expandState struct {
	runner          chartRunner
	resolver        chartSourceResolver
	scheme          *runtime.Scheme
	render          *render.Render
	releases        []*helmv2.HelmRelease
	repositories    []*sourcev1.HelmRepository
	ociRepositories []*sourcev1.OCIRepository
	logger          logr.Logger
}

// NewExpander creates a new Helm expander.
func NewExpander(runner *Runner, resolver chartSourceResolver, log logr.Logger) *Expander {
	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)
	return &Expander{
		runner:   runner,
		resolver: resolver,
		logger:   log,
		scheme:   sch,
	}
}

// Expand implements expander.Expander. It parses HelmRelease and source resources
// from the render, then delegates chart rendering to the Runner.
func (e *Expander) Expand(ctx context.Context, r *render.Render) (*expander.ExpandResult, error) {

	if e.expanded == nil {
		e.expanded = make(map[string]bool)
	}

	s := &expandState{
		runner:   e.runner,
		resolver: e.resolver,
		scheme:   e.scheme,
		render:   r,
		logger:   e.logger,
	}

	// Parse resources from the render
	for _, res := range r.Resources() {
		gvk := res.GetGvk()
		s.logger.V(1).Info("found manifest", "group", gvk.Group, "kind", gvk.Kind, "version", gvk.Version)

		if matchGVK(gvk, helmReleaseGVK) {
			release, err := s.parseHelmRelease(res)
			if err != nil {
				return nil, fmt.Errorf("error parsing HelmRelease: %w", err)
			}
			key := release.Namespace + "/" + release.Name
			if e.expanded[key] {
				s.logger.V(1).Info("skipping already-expanded HelmRelease", "name", release.Name, "namespace", release.Namespace)
				continue
			}
			e.expanded[key] = true
			s.logger.V(1).Info("found helm release", "name", release.Name, "namespace", release.Namespace)
			s.releases = append(s.releases, release)
		} else if matchGVK(gvk, helmRepoGVK) {
			repo, err := s.parseRepository(res)
			if err != nil {
				return nil, fmt.Errorf("error parsing HelmRepository: %w", err)
			}
			s.logger.V(1).Info("found helm repository", "name", repo.Name, "namespace", repo.Namespace)
			s.repositories = append(s.repositories, repo)
		} else if matchGVK(gvk, ociRepoGVK) {
			repo, err := s.parseOCIRepository(res)
			if err != nil {
				return nil, fmt.Errorf("error parsing OCIRepository: %w", err)
			}
			s.logger.V(1).Info("found oci repository", "name", repo.Name, "namespace", repo.Namespace)
			s.ociRepositories = append(s.ociRepositories, repo)
		}
	}

	resources, errs, err := s.renderAllCharts(ctx)
	if err != nil {
		return nil, err
	}
	return &expander.ExpandResult{Resources: resources, Errors: errs}, nil
}

func (s *expandState) parseHelmRelease(res *resource.Resource) (*helmv2.HelmRelease, error) {
	hr, err := decodeResource[helmv2.HelmRelease](res)
	if err != nil {
		return nil, err
	}
	if hr.Spec.Chart == nil {
		return nil, fmt.Errorf("HelmRelease %s/%s has no spec.chart", hr.Namespace, hr.Name)
	}
	return hr, nil
}

func (s *expandState) parseRepository(res *resource.Resource) (*sourcev1.HelmRepository, error) {
	repo, err := decodeResource[sourcev1.HelmRepository](res)
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func (s *expandState) parseOCIRepository(res *resource.Resource) (*sourcev1.OCIRepository, error) {
	repo, err := decodeResource[sourcev1.OCIRepository](res)
	if err != nil {
		return nil, err
	}
	return repo, nil
}
func (s *expandState) renderAllCharts(ctx context.Context) (resmap.ResMap, []error, error) {
	var tasks []RenderTask
	var skipErrs []error
	valuesClient, err := s.newValuesClient()
	if err != nil {
		return nil, nil, fmt.Errorf("building values client: %w", err)
	}
	for _, h := range s.releases {
		values, err := s.composeValues(ctx, valuesClient, h)
		if err != nil {
			return nil, nil, fmt.Errorf("error composing values for %s/%s: %w", h.Namespace, h.Name, err)
		}
		src, err := s.findChartUrl(h)
		if err != nil {
			s.logger.V(1).Info("skipping HelmRelease, cannot resolve chart source", "name", h.Name, "namespace", h.Namespace, "error", err)
			skipErrs = append(skipErrs, fmt.Errorf("HelmRelease %s/%s: %w", h.Namespace, h.Name, err))
			continue
		}

		chartName := h.Spec.Chart.Spec.Chart
		chartVersion := h.Spec.Chart.Spec.Version
		releaseName := h.Spec.ReleaseName
		if releaseName == "" {
			releaseName = h.Name
		}
		namespace := h.Spec.TargetNamespace
		if namespace == "" {
			namespace = h.Namespace
		}

		install := h.GetInstall()
		postRenderer := buildPostRenderers(h)
		tasks = append(tasks, RenderTask{
			values:          values,
			chart:           chartName,
			version:         chartVersion,
			repo:            repo.Entry{URL: src.url, Name: fmt.Sprintf("%s-%s", h.Namespace, h.Name)},
			localChartPath:  src.localPath,
			releaseName:     releaseName,
			namespace:       namespace,
			skipCRDs:        install.SkipCRDs,
			replace:         install.Replace,
			disableHooks:    install.DisableHooks,
			createNamespace: install.CreateNamespace,
			isOCI:           src.isOCI,
			postRenderer:    postRenderer,
		})
	}

	if len(tasks) == 0 {
		return resmap.New(), skipErrs, nil
	}
	resources, renderErrs, err := s.runner.RenderCharts(ctx, tasks)
	if err != nil {
		return nil, nil, err
	}
	return resources, append(skipErrs, renderErrs...), nil
}

func (s *expandState) composeValues(ctx context.Context, valuesClient ctrlclient.Client, hr *helmv2.HelmRelease) (chartcommon.Values, error) {
	inlineValues := hr.GetValues()
	if len(hr.Spec.ValuesFrom) == 0 {
		return inlineValues, nil
	}
	values, err := fluxchartutil.ChartValuesFromReferences(
		ctx,
		s.logger.WithValues("release", hr.Name, "namespace", hr.Namespace),
		valuesClient,
		hr.Namespace,
		inlineValues,
		hr.Spec.ValuesFrom...,
	)
	if err != nil {
		return nil, err
	}
	return values, nil
}

// chartSource holds the resolved chart location and whether it's an OCI reference.
type chartSource struct {
	url       string
	isOCI     bool
	localPath string
}

func (s *expandState) findChartUrl(source *helmv2.HelmRelease) (chartSource, error) {
	if source.Spec.Chart == nil {
		return chartSource{}, fmt.Errorf("HelmRelease %s/%s has no chart.spec.sourceRef", source.Namespace, source.Name)
	}
	sourceRef := source.Spec.Chart.Spec.SourceRef
	namespace := sourceRef.Namespace
	if namespace == "" {
		namespace = source.Namespace
	}
	name := sourceRef.Name
	kind := sourceRef.Kind

	switch kind {
	case "HelmRepository":
		for _, hr := range s.repositories {
			if hr.Namespace == namespace && hr.Name == name {
				return chartSource{url: hr.Spec.URL, isOCI: hr.Spec.Type == "oci"}, nil
			}
		}
	case "OCIRepository":
		for _, oci := range s.ociRepositories {
			if oci.Namespace == namespace && oci.Name == name {
				return chartSource{url: oci.Spec.URL, isOCI: true}, nil
			}
		}
	case "GitRepository":
		if s.resolver == nil {
			s.logger.V(1).Info("skipping HelmRelease with GitRepository chart source (requires --resolve-git)", "name", source.Name, "namespace", source.Namespace)
			return chartSource{}, nil
		}
		baseDir, ok := s.resolver.ResolvePath(namespace, name)
		if !ok {
			s.logger.V(1).Info("skipping HelmRelease with unresolved GitRepository chart source", "name", name, "namespace", namespace)
			return chartSource{}, nil
		}
		chartPath := strings.TrimPrefix(source.Spec.Chart.Spec.Chart, "./")
		if chartPath == "" {
			return chartSource{}, fmt.Errorf("HelmRelease %s/%s has no chart.spec.chart path", source.Namespace, source.Name)
		}
		return chartSource{localPath: filepath.Join(baseDir, chartPath)}, nil
	default:
		return chartSource{}, fmt.Errorf("unsupported source kind '%s'", kind)
	}
	return chartSource{}, fmt.Errorf("unable to find source '%s'", name)
}

func decodeResource[T any](res *resource.Resource) (*T, error) {
	var out T
	m, err := res.Map()
	if err != nil {
		return nil, err
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(m, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *expandState) newValuesClient() (ctrlclient.Client, error) {
	objects := make([]ctrlclient.Object, 0)
	for _, res := range s.render.Resources() {
		switch res.GetGvk() {
		case configMapGVK:
			var cm corev1.ConfigMap
			if err := s.convertResource(res, &cm); err != nil {
				return nil, err
			}
			objects = append(objects, &cm)
		case secretGVK:
			var secret corev1.Secret
			if err := s.convertResource(res, &secret); err != nil {
				return nil, err
			}
			objects = append(objects, &secret)
		}
	}
	return fake.NewClientBuilder().WithScheme(s.scheme).WithObjects(objects...).Build(), nil
}

func (s *expandState) convertResource(res *resource.Resource, to any) error {
	m, err := res.Map()
	if err != nil {
		return err
	}
	var u unstructured.Unstructured
	u.SetUnstructuredContent(m)
	return s.scheme.Convert(&u, to, nil)
}
