package render

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

const ProducerAnnotation = "fmp.tobiash.github.io/producer"

var postBuildSubstitutionPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Provenance identifies the Flux rendering source that produced a resource.
type Provenance struct {
	Kind      string
	Name      string
	Namespace string
	Path      string
	Text      string
}

// String returns the stable display form used by reports and policy checks.
func (p Provenance) String() string {
	if p.Text != "" {
		return p.Text
	}
	switch {
	case p.Kind == "HelmRelease" && p.Namespace != "" && p.Name != "":
		return fmt.Sprintf("HelmRelease %s/%s", p.Namespace, p.Name)
	case p.Kind != "" && p.Namespace != "" && p.Name != "":
		return fmt.Sprintf("%s %s/%s", p.Kind, p.Namespace, p.Name)
	case p.Kind != "" && p.Name != "":
		return fmt.Sprintf("%s %s", p.Kind, p.Name)
	case p.Path != "":
		return fmt.Sprintf("path %s", p.Path)
	default:
		return ""
	}
}

// PathProvenance describes resources rendered from an explicit path.
func PathProvenance(path string) Provenance {
	return Provenance{Kind: "Path", Path: path, Text: fmt.Sprintf("path %s", path)}
}

// TextProvenance preserves existing producer strings while centralizing provenance storage.
func TextProvenance(text string) Provenance {
	return Provenance{Text: text}
}

// HelmReleaseProvenance describes resources rendered from a Flux HelmRelease.
func HelmReleaseProvenance(namespace, name string) Provenance {
	return Provenance{Kind: "HelmRelease", Namespace: namespace, Name: name}
}

// MatchGVK reports true if the resource's GVK matches the target by group and kind.
// Version is ignored because Flux resources exist in multiple API versions.
func MatchGVK(resGvk, target resid.Gvk) bool {
	return resGvk.Group == target.Group && resGvk.Kind == target.Kind
}

// Render holds a set of rendered Kubernetes YAML resources.
type Render struct {
	resmap.ResMap
	kustomizer *krusty.Kustomizer
	log        logr.Logger
	warnings   []error
	provenance map[string]Provenance
}

// ResourceView is the domain view of a rendered Kubernetes resource plus fmp metadata.
type ResourceView struct {
	ID         resid.ResId
	Kind       string
	Name       string
	Namespace  string
	Provenance Provenance
	Producer   string
	Object     map[string]any
	YAML       string
}

// NewDefaultRender creates a Render with default kustomize options.
func NewDefaultRender(log logr.Logger) *Render {
	return &Render{
		ResMap:     resmap.New(),
		kustomizer: krusty.MakeKustomizer(krusty.MakeDefaultOptions()),
		log:        log,
		provenance: make(map[string]Provenance),
	}
}

// AddKustomization runs kustomize on the given path and appends the results.
func (r *Render) AddKustomization(fSys filesys.FileSystem, path string) error {
	return r.AddKustomizationWithProducer(fSys, path, PathProvenance(path).String())
}

// AddKustomizationWithProducer runs kustomize on the given path and records the producer.
func (r *Render) AddKustomizationWithProducer(fSys filesys.FileSystem, path, producer string) error {
	resmap, err := r.kustomizer.Run(fSys, path)
	if err != nil {
		return err
	}
	return r.absorbResMap(path, producer, resmap)
}

// AddPath loads resources from a directory path. If the directory contains a
// kustomization file (kustomization.yaml, kustomization.yml, or Kustomization),
// it is processed as a kustomize base. Otherwise all .yaml/.yml files in the
// directory are loaded as raw Kubernetes manifests.
func (r *Render) AddPath(fSys filesys.FileSystem, path string) error {
	return r.AddPathWithProducer(fSys, path, PathProvenance(path).String())
}

// AddPathWithProducer loads resources from a path and records the producer.
func (r *Render) AddPathWithProducer(fSys filesys.FileSystem, path, producer string) error {
	if isKustomization(fSys, path) {
		return r.AddKustomizationWithProducer(fSys, path, producer)
	}
	return r.addRawYAMLFiles(fSys, path, producer)
}

func (r *Render) addRawYAMLFiles(fSys filesys.FileSystem, dir, producer string) error {
	entries, err := fSys.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", dir, err)
	}

	for _, name := range entries {
		// These are fmp configuration files, not Kubernetes manifests.
		if name == ".fmp.yaml" || name == ".fmp.yml" || (filepath.Base(dir) == ".github" && name == "fmp.yaml") {
			continue
		}
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		fullPath := filepath.Join(dir, name)
		data, err := fSys.ReadFile(fullPath)
		if err != nil {
			return fmt.Errorf("reading %s: %w", fullPath, err)
		}

		resources, err := resmap.NewFactory(resource.NewFactory(nil)).NewResMapFromBytes(data)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", fullPath, err)
		}

		if err := r.absorbResMap(fullPath, producer, resources); err != nil {
			return fmt.Errorf("appending resources from %s: %w", fullPath, err)
		}
	}

	return nil
}

func (r *Render) absorbResMap(source, producer string, src resmap.ResMap) error {
	for _, res := range src.Resources() {
		id := res.CurId()
		idKey := id.String()
		newProvenance := provenanceForResource(res, TextProvenance(producer))
		if rendered, ok := src.(*Render); ok {
			if p := rendered.ProvenanceForID(id); p.String() != "" {
				newProvenance = p
			}
		}
		if existing, err := r.GetById(id); err == nil {
			existingProvenance := r.provenance[idKey]
			if existingProvenance.String() == "" {
				existingProvenance = provenanceForResource(existing, TextProvenance("existing resources"))
			}
			_ = r.Remove(existing.CurId())
			r.warnings = append(r.warnings, duplicateWarning(id.String(), existingProvenance.String(), newProvenance.String(), source))
			r.log.V(1).Info("replacing duplicate resource", "id", id)
		}
		if err := r.Append(res); err != nil {
			return err
		}
		r.provenance[idKey] = newProvenance
	}
	return nil
}

// Warnings returns non-fatal issues encountered while building the resource set.
func (r *Render) Warnings() []error {
	return append([]error(nil), r.warnings...)
}

// AbsorbAll merges resources into the render, replacing duplicates and recording warnings.
func (r *Render) AbsorbAll(src resmap.ResMap) error {
	if rendered, ok := src.(*Render); ok {
		r.warnings = append(r.warnings, rendered.Warnings()...)
	}
	return r.absorbResMap("expanded resources", "expanded resources", src)
}

// AddPaths recursively loads resources from a directory and all subdirectories.
// Each subdirectory is processed independently -- directories with a
// kustomization file are processed as kustomize bases; directories without one
// have their .yaml/.yml files loaded as raw manifests.
// When a directory is processed as a kustomize base, its subdirectories are
// not recursed into because kustomize already handles resource loading.
func (r *Render) AddPaths(fSys filesys.FileSystem, root string) error {
	return r.AddPathsWithProducer(fSys, root, PathProvenance(root).String())
}

// AddPathsWithProducer recursively loads resources from a directory and records the producer.
func (r *Render) AddPathsWithProducer(fSys filesys.FileSystem, root, producer string) error {
	isKust := isKustomization(fSys, root)
	if isKust {
		if err := r.AddKustomizationWithProducer(fSys, root, producer); err != nil {
			return err
		}
	} else {
		if err := r.addRawYAMLFiles(fSys, root, producer); err != nil {
			return err
		}
	}

	// Only recurse into subdirectories if this was not a kustomize base.
	// Kustomize already loads referenced resources from subdirectories.
	if isKust {
		return nil
	}

	entries, err := fSys.ReadDir(root)
	if err != nil {
		return fmt.Errorf("reading directory %s: %w", root, err)
	}

	for _, name := range entries {
		sub := filepath.Join(root, name)
		if fSys.IsDir(sub) {
			if err := r.AddPathsWithProducer(fSys, sub, producer); err != nil {
				return err
			}
		}
	}

	return nil
}

// MarkProvenanceToNew records producer metadata for resources added after count.
func (r *Render) MarkProvenanceToNew(count int, producer string) {
	for _, res := range r.Resources()[count:] {
		r.provenance[res.CurId().String()] = provenanceForResource(res, TextProvenance(producer))
	}
}

// ApplySubstitutionsToNew applies Flux postBuild inline substitutions to
// resources added after count. Missing variables are intentionally preserved so
// the report still shows unresolved substituteFrom or strict-mode inputs.
func (r *Render) ApplySubstitutionsToNew(count int, producer string, substitutions map[string]string) error {
	if len(substitutions) == 0 {
		return nil
	}

	newResources := append([]*resource.Resource(nil), r.Resources()[count:]...)
	for _, res := range newResources {
		delete(r.provenance, res.CurId().String())
		if err := r.Remove(res.CurId()); err != nil {
			return err
		}
	}

	for _, res := range newResources {
		yaml := applyPostBuildSubstitutions(res.MustYaml(), substitutions)
		resources, err := resmap.NewFactory(resource.NewFactory(nil)).NewResMapFromBytes([]byte(yaml))
		if err != nil {
			return fmt.Errorf("applying postBuild substitutions to %s: %w", res.CurId(), err)
		}
		if err := r.absorbResMap("postBuild substitutions", producer, resources); err != nil {
			return err
		}
	}
	return nil
}

func applyPostBuildSubstitutions(input string, substitutions map[string]string) string {
	return postBuildSubstitutionPattern.ReplaceAllStringFunc(input, func(token string) string {
		expr := token[2 : len(token)-1]
		if key, fallback, ok := strings.Cut(expr, ":="); ok {
			if value, exists := substitutions[key]; exists && value != "" {
				return value
			}
			return fallback
		}
		if key, fallback, ok := strings.Cut(expr, ":-"); ok {
			if value, exists := substitutions[key]; exists && value != "" {
				return value
			}
			return fallback
		}
		if value, ok := substitutions[expr]; ok {
			return value
		}
		return token
	})
}

// ProvenanceForID returns the structured provenance for a resource ID.
func (r *Render) ProvenanceForID(id resid.ResId) Provenance {
	return r.provenance[id.String()]
}

// ResourceViewForID returns a rendered resource and its fmp metadata.
func (r *Render) ResourceViewForID(id resid.ResId) (ResourceView, bool) {
	res, _ := r.GetByCurrentId(id)
	if res == nil {
		return ResourceView{}, false
	}
	obj, _ := res.Map()
	provenance := r.ProvenanceForID(id)
	return ResourceView{
		ID:         id,
		Kind:       res.GetKind(),
		Name:       res.GetName(),
		Namespace:  res.GetNamespace(),
		Provenance: provenance,
		Producer:   provenance.String(),
		Object:     obj,
		YAML:       res.MustYaml(),
	}, true
}

func duplicateWarning(id, existingProducer, newProducer, source string) error {
	if existingProducer == "" {
		existingProducer = "existing resources"
	}
	if newProducer == "" {
		newProducer = source
	}
	if existingProducer == newProducer {
		return fmt.Errorf("duplicate resource %s produced by %s replaced an earlier instance from the same producer", id, newProducer)
	}
	return fmt.Errorf("duplicate resource %s produced by %s replaced an existing resource produced by %s", id, newProducer, existingProducer)
}

func provenanceForResource(res *resource.Resource, fallback Provenance) Provenance {
	annotations := res.GetAnnotations()
	if producer := annotations[ProducerAnnotation]; producer != "" {
		return TextProvenance(producer)
	}

	labels := res.GetLabels()
	if name := labels["helm.toolkit.fluxcd.io/name"]; name != "" {
		ns := labels["helm.toolkit.fluxcd.io/namespace"]
		if ns == "" {
			ns = res.GetNamespace()
		}
		return HelmReleaseProvenance(ns, name)
	}
	return fallback
}

// isKustomization checks whether a directory contains a kustomization file.
func isKustomization(fSys filesys.FileSystem, path string) bool {
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if fSys.Exists(filepath.Join(path, name)) {
			return true
		}
	}
	return false
}

// Sort orders resources by (kind, namespace, name) for deterministic output.
// This is critical for diff stability across runs.
func (r *Render) Sort() {
	resources := r.Resources()
	sort.Slice(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		if a.GetKind() != b.GetKind() {
			return a.GetKind() < b.GetKind()
		}
		if a.GetNamespace() != b.GetNamespace() {
			return a.GetNamespace() < b.GetNamespace()
		}
		return a.GetName() < b.GetName()
	})
	r.Clear()
	for _, res := range resources {
		_ = r.Append(res)
	}
}

// FilterByLabel removes all resources that do not have the given label key
// set to the given value. Resources without the label are removed.
func (r *Render) FilterByLabel(key, value string) {
	for _, res := range r.Resources() {
		if res.GetLabels()[key] != value {
			_ = r.Remove(res.CurId())
		}
	}
}

// FilterCRDs removes all CustomResourceDefinition resources from the render.
func (r *Render) FilterCRDs() {
	for _, res := range r.Resources() {
		if res.GetKind() == "CustomResourceDefinition" {
			_ = r.Remove(res.CurId())
		}
	}
}

// ApplyNamespaceToNew sets the namespace on all namespace-scoped resources
// added after the given count. This is used to apply Flux Kustomization
// spec.targetNamespace to newly rendered resources.
func (r *Render) ApplyNamespaceToNew(count int, namespace string) error {
	clusterScoped := make(map[string]bool)
	for _, res := range r.Resources() {
		if res.GetKind() != "CustomResourceDefinition" || res.GetGvk().Group != "apiextensions.k8s.io" {
			continue
		}
		scope, _ := res.GetFieldValue("spec.scope")
		group, _ := res.GetFieldValue("spec.group")
		kind, _ := res.GetFieldValue("spec.names.kind")
		if scope == "Cluster" {
			clusterScoped[fmt.Sprint(group)+"/"+fmt.Sprint(kind)] = true
		}
	}
	resources := append([]*resource.Resource(nil), r.Resources()[count:]...)
	producers := make([]Provenance, len(resources))
	for i, res := range resources {
		producers[i] = r.ProvenanceForID(res.CurId())
		delete(r.provenance, res.CurId().String())
		if err := r.Remove(res.CurId()); err != nil {
			return err
		}
	}
	for i, res := range resources {
		gvk := res.GetGvk()
		if !gvk.IsClusterScoped() && !clusterScoped[gvk.Group+"/"+gvk.Kind] && (gvk.Group != "snapshot.storage.k8s.io" || gvk.Kind != "VolumeSnapshotClass") {
			if err := res.SetNamespace(namespace); err != nil {
				return err
			}
		}
		one := resmap.New()
		if err := one.Append(res); err != nil {
			return err
		}
		if err := r.absorbResMap("targetNamespace", producers[i].String(), one); err != nil {
			return err
		}
	}
	return nil
}

// AsJSON returns the rendered resources as a Kubernetes List envelope.
func (r *Render) AsJSON() ([]byte, error) {
	resources := r.Resources()
	items := make([]map[string]any, 0, len(resources))
	for i, res := range resources {
		m, err := res.Map()
		if err != nil {
			return nil, fmt.Errorf("converting resource %d to JSON map: %w", i+1, err)
		}
		items = append(items, m)
	}
	list := map[string]any{
		"apiVersion": "v1",
		"kind":       "List",
		"items":      items,
	}
	return json.MarshalIndent(list, "", "  ")
}
