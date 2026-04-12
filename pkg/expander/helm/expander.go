package helm

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	"helm.sh/helm/v4/pkg/repo/v1"
	"helm.sh/helm/v4/pkg/strvals"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
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
// Version is ignored because Flux resources exist in multiple API versions
// (e.g. v2beta1, v2, v1beta2, v1) and conversion between them requires a
// running API server with conversion webhooks. We work with unstructured
// data directly instead.
func matchGVK(resGvk resid.Gvk, target resid.Gvk) bool {
	return resGvk.Group == target.Group && resGvk.Kind == target.Kind
}

// Expander implements the expander.Expander interface for Helm.
// It is safe for concurrent use -- each call to Expand operates on isolated state.
// The Expander tracks which releases have already been expanded to avoid
// duplicating resources across iterative expansion loops.
type Expander struct {
	runner   *Runner
	logger   logr.Logger
	scheme   *runtime.Scheme
	expanded map[string]bool // "namespace/name" of already-expanded releases
}

// expandState holds per-invocation state for a single Expand call.
type expandState struct {
	runner          *Runner
	scheme          *runtime.Scheme
	render          *render.Render
	releases        []unstructuredRelease
	repositories    []unstructuredRepository
	ociRepositories []unstructuredRepository
	logger          logr.Logger
}

// unstructuredRelease holds the fields we need from a HelmRelease,
// extracted from unstructured data to avoid API version conversion issues.
type unstructuredRelease struct {
	name      string
	namespace string
	spec      map[string]any
}

// unstructuredRepository holds the fields we need from a HelmRepository or OCIRepository.
type unstructuredRepository struct {
	name      string
	namespace string
	url       string
	isOCI     bool
}

// NewExpander creates a new Helm expander.
func NewExpander(runner *Runner, log logr.Logger) *Expander {
	sch := runtime.NewScheme()
	_ = scheme.AddToScheme(sch)
	return &Expander{
		runner: runner,
		logger: log,
		scheme: sch,
	}
}

// Expand implements expander.Expander. It parses HelmRelease and source resources
// from the render, then delegates chart rendering to the Runner.
func (e *Expander) Expand(ctx context.Context, r *render.Render) (*expander.ExpandResult, error) {

	if e.expanded == nil {
		e.expanded = make(map[string]bool)
	}

	s := &expandState{
		runner: e.runner,
		scheme: e.scheme,
		render: r,
		logger: e.logger,
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
			key := release.namespace + "/" + release.name
			if e.expanded[key] {
				s.logger.V(1).Info("skipping already-expanded HelmRelease", "name", release.name, "namespace", release.namespace)
				continue
			}
			e.expanded[key] = true
			s.logger.V(1).Info("found helm release", "name", release.name, "namespace", release.namespace)
			s.releases = append(s.releases, release)
		} else if matchGVK(gvk, helmRepoGVK) {
			repo, err := s.parseRepository(res)
			if err != nil {
				return nil, fmt.Errorf("error parsing HelmRepository: %w", err)
			}
			s.logger.V(1).Info("found helm repository", "name", repo.name, "namespace", repo.namespace)
			s.repositories = append(s.repositories, repo)
		} else if matchGVK(gvk, ociRepoGVK) {
			repo, err := s.parseRepository(res)
			if err != nil {
				return nil, fmt.Errorf("error parsing OCIRepository: %w", err)
			}
			s.logger.V(1).Info("found oci repository", "name", repo.name, "namespace", repo.namespace)
			s.ociRepositories = append(s.ociRepositories, repo)
		}
	}

	resources, err := s.renderAllCharts(ctx)
	if err != nil {
		return nil, err
	}
	return &expander.ExpandResult{Resources: resources}, nil
}

func (s *expandState) parseHelmRelease(res *resource.Resource) (unstructuredRelease, error) {
	m, err := res.Map()
	if err != nil {
		return unstructuredRelease{}, err
	}
	u := &unstructured.Unstructured{}
	u.SetUnstructuredContent(m)
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return unstructuredRelease{}, fmt.Errorf("HelmRelease %s/%s has no spec", u.GetNamespace(), u.GetName())
	}
	return unstructuredRelease{
		name:      u.GetName(),
		namespace: u.GetNamespace(),
		spec:      spec,
	}, nil
}

func (s *expandState) parseRepository(res *resource.Resource) (unstructuredRepository, error) {
	m, err := res.Map()
	if err != nil {
		return unstructuredRepository{}, err
	}
	u := &unstructured.Unstructured{}
	u.SetUnstructuredContent(m)
	spec, ok := m["spec"].(map[string]any)
	if !ok {
		return unstructuredRepository{}, fmt.Errorf("%s %s/%s has no spec", res.GetKind(), u.GetNamespace(), u.GetName())
	}
	url, _, _ := unstructured.NestedString(spec, "url")
	typ, _, _ := unstructured.NestedString(spec, "type")
	return unstructuredRepository{
		name:      u.GetName(),
		namespace: u.GetNamespace(),
		url:       url,
		isOCI:     typ == "oci" || res.GetKind() == "OCIRepository",
	}, nil
}
func (s *expandState) renderAllCharts(ctx context.Context) (resmap.ResMap, error) {
	var tasks []RenderTask
	for _, h := range s.releases {
		values, err := s.composeValues(h)
		if err != nil {
			return nil, fmt.Errorf("error composing values for %s/%s: %w", h.namespace, h.name, err)
		}
		src, err := s.findChartUrl(h)
		if err != nil {
			s.logger.Info("skipping HelmRelease, cannot resolve chart source", "name", h.name, "namespace", h.namespace, "error", err)
			continue
		}

		chartName := nestedString(h.spec, "chart", "spec", "chart")
		chartVersion := nestedString(h.spec, "chart", "spec", "version")

		// Resolve semver ranges to concrete versions.
		if resolved, err := s.runner.ResolveVersion(src.url, chartName, chartVersion); err == nil {
			chartVersion = resolved
		} else {
			s.logger.Info("version range resolution failed, using raw version", "chart", chartName, "version", chartVersion, "error", err)
		}
		releaseName := nestedString(h.spec, "releaseName")
		if releaseName == "" {
			releaseName = h.name
		}
		targetNamespace := nestedString(h.spec, "targetNamespace")
		namespace := targetNamespace
		if namespace == "" {
			namespace = h.namespace
		}

		install := nestedMap(h.spec, "install")
		tasks = append(tasks, RenderTask{
			values:          values,
			chart:           chartName,
			version:         chartVersion,
			repo:            repo.Entry{URL: src.url, Name: fmt.Sprintf("%s-%s", h.namespace, h.name)},
			releaseName:     releaseName,
			namespace:       namespace,
			skipCRDs:        nestedBoolDefault(install, false, "skipCRDs"),
			replace:         nestedBoolDefault(install, false, "replace"),
			disableHooks:    nestedBoolDefault(install, false, "disableHooks"),
			createNamespace: nestedBoolDefault(install, false, "createNamespace"),
			isOCI:           src.isOCI,
		})
	}

	if len(tasks) == 0 {
		return resmap.New(), nil
	}
	return s.runner.RenderCharts(ctx, tasks)
}

func (s *expandState) composeValues(hr unstructuredRelease) (chartcommon.Values, error) {
	var result chartcommon.Values
	logger := s.logger.WithValues("release", hr.name, "namespace", hr.namespace)

	valuesFrom, ok := hr.spec["valuesFrom"].([]any)
	if !ok {
		valuesFrom = nil
	}

	for _, vIf := range valuesFrom {
		vMap, ok := vIf.(map[string]any)
		if !ok {
			continue
		}
		kind, _, _ := unstructured.NestedString(vMap, "kind")
		name, _, _ := unstructured.NestedString(vMap, "name")
		targetPath, _, _ := unstructured.NestedString(vMap, "targetPath")
		valuesKey, _, _ := unstructured.NestedString(vMap, "valuesKey")
		if valuesKey == "" {
			valuesKey = "values.yaml"
		}

		namespacedName := types.NamespacedName{Namespace: hr.namespace, Name: name}
		var valuesData []byte

		switch kind {
		case "ConfigMap":
			var cm corev1.ConfigMap
			found, err := s.findResource(configMapGVK, namespacedName, &cm)
			if err != nil {
				return nil, fmt.Errorf("error loading configmap %s: %w", namespacedName, err)
			}
			if !found {
				logger.Info("configmap not found, ignoring values", "configmap", namespacedName)
				continue
			}
			if data, ok := cm.Data[valuesKey]; !ok {
				return nil, fmt.Errorf("missing key '%s' in %s '%s'", kind, valuesKey, namespacedName)
			} else {
				valuesData = []byte(data)
			}
		case "Secret":
			var secret corev1.Secret
			found, err := s.findResource(secretGVK, namespacedName, &secret)
			if err != nil {
				return nil, fmt.Errorf("error loading secret %s: %w", namespacedName, err)
			}
			if !found {
				logger.Info("secret not found, ignoring values", "secret", namespacedName)
				continue
			}
			if data, ok := secret.Data[valuesKey]; !ok {
				return nil, fmt.Errorf("missing key '%s' in %s '%s'", kind, valuesKey, namespacedName)
			} else {
				valuesData = []byte(data)
			}
		default:
			return nil, fmt.Errorf("unsupported ValuesReference kind '%s'", kind)
		}

		switch targetPath {
		case "":
			values, err := chartcommon.ReadValues(valuesData)
			if err != nil {
				return nil, fmt.Errorf("error reading values from %s '%s': %w", kind, namespacedName, err)
			}
			result = mergeMaps(result, values)
		default:
			stringValuesData := string(valuesData)
			singleQuote := "'"
			doubleQuote := "\""
			var err error
			if (strings.HasPrefix(stringValuesData, singleQuote) && strings.HasSuffix(stringValuesData, singleQuote)) ||
				(strings.HasPrefix(stringValuesData, doubleQuote) && strings.HasSuffix(stringValuesData, doubleQuote)) {
				stringValuesData = strings.Trim(stringValuesData, singleQuote+doubleQuote)
				singleValue := targetPath + "=" + stringValuesData
				err = strvals.ParseIntoString(singleValue, result)
			} else {
				singleValue := targetPath + "=" + stringValuesData
				err = strvals.ParseInto(singleValue, result)
			}
			if err != nil {
				return nil, fmt.Errorf("unable to merge value from key '%s' in %s '%s' into target path '%s': %w",
					valuesKey, kind, namespacedName, targetPath, err)
			}
		}
	}

	// Merge inline values from spec.values
	if inlineValues, ok := hr.spec["values"].(map[string]any); ok {
		result = mergeMaps(result, inlineValues)
	}

	return result, nil
}

// chartSource holds the resolved chart location and whether it's an OCI reference.
type chartSource struct {
	url   string
	isOCI bool
}

func (s *expandState) findChartUrl(source unstructuredRelease) (chartSource, error) {
	sourceRef := nestedMap(source.spec, "chart", "spec", "sourceRef")
	if sourceRef == nil {
		return chartSource{}, fmt.Errorf("HelmRelease %s/%s has no chart.spec.sourceRef", source.namespace, source.name)
	}

	namespace, _ := sourceRef["namespace"].(string)
	if namespace == "" {
		namespace = source.namespace
	}
	name, _ := sourceRef["name"].(string)
	kind, _ := sourceRef["kind"].(string)

	switch kind {
	case "HelmRepository":
		for _, hr := range s.repositories {
			if hr.namespace == namespace && hr.name == name {
				return chartSource{url: hr.url, isOCI: hr.isOCI}, nil
			}
		}
	case "OCIRepository":
		for _, oci := range s.ociRepositories {
			if oci.namespace == namespace && oci.name == name {
				return chartSource{url: oci.url, isOCI: true}, nil
			}
		}
	default:
		return chartSource{}, fmt.Errorf("unsupported source kind '%s'", kind)
	}
	return chartSource{}, fmt.Errorf("unable to find source '%s'", name)
}

func (s *expandState) findResource(gvk resid.Gvk, namespacedName types.NamespacedName, to any) (bool, error) {
	res, err := s.render.GetById(resid.NewResIdWithNamespace(gvk, namespacedName.Name, namespacedName.Namespace))
	if err != nil {
		return false, nil
	}
	var u unstructured.Unstructured
	m, err := res.Map()
	if err != nil {
		return false, err
	}
	u.SetUnstructuredContent(m)
	return true, s.scheme.Convert(&u, to, nil)
}

// nestedField extracts a value from a nested map following the given key path.
// Returns (nil, false) if any key is missing. Does not deep-copy.
func nestedField(m map[string]any, keys ...string) (any, bool) {
	for i, key := range keys {
		val, ok := m[key]
		if !ok {
			return nil, false
		}
		if i == len(keys)-1 {
			return val, true
		}
		next, ok := val.(map[string]any)
		if !ok {
			return nil, false
		}
		m = next
	}
	return nil, false
}

// nestedString extracts a nested string from a map following the given key path.
func nestedString(m map[string]any, keys ...string) string {
	val, _ := nestedField(m, keys...)
	if s, ok := val.(string); ok {
		return s
	}
	return ""
}

// nestedMap extracts a nested map from a map following the given key path.
// Unlike unstructured.NestedMap, this does not deep-copy which avoids
// panics on non-JSON-safe types (e.g. raw int).
func nestedMap(m map[string]any, keys ...string) map[string]any {
	for i, key := range keys {
		if i == len(keys)-1 {
			val, ok := m[key]
			if !ok {
				return nil
			}
			result, ok := val.(map[string]any)
			if !ok {
				return nil
			}
			return result
		}
		next, ok := m[key]
		if !ok {
			return nil
		}
		m, ok = next.(map[string]any)
		if !ok {
			return nil
		}
	}
	return m
}

// nestedBoolDefault extracts a nested bool, returning the default if not found.
func nestedBoolDefault(m map[string]any, def bool, keys ...string) bool {
	val, _ := nestedField(m, keys...)
	if b, ok := val.(bool); ok {
		return b
	}
	return def
}

// mergeMaps recursively merges overlay into base, returning a new map.
func mergeMaps(base, overlay map[string]any) map[string]any {
	result := make(map[string]any)
	for k, v := range base {
		result[k] = v
	}
	for k, v := range overlay {
		if existing, ok := result[k]; ok {
			if existingMap, ok := existing.(map[string]any); ok {
				if overlayMap, ok := v.(map[string]any); ok {
					result[k] = mergeMaps(existingMap, overlayMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}
