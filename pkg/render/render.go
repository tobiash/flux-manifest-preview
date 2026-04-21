package render

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/go-logr/logr"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Render holds a set of rendered Kubernetes YAML resources.
type Render struct {
	resmap.ResMap
	kustomizer *krusty.Kustomizer
	log        logr.Logger
	warnings   []error
	provenance map[string]string
}

// NewDefaultRender creates a Render with default kustomize options.

func NewDefaultRender(log logr.Logger) *Render {
	return &Render{
		ResMap:     resmap.New(),
		kustomizer: krusty.MakeKustomizer(krusty.MakeDefaultOptions()),
		log:        log,
		provenance: make(map[string]string),
	}
}

// AddKustomization runs kustomize on the given path and appends the results.

func (r *Render) AddKustomization(fSys filesys.FileSystem, path string) error {
	return r.AddKustomizationWithProducer(fSys, path, fmt.Sprintf("path %s", path))
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
	return r.AddPathWithProducer(fSys, path, fmt.Sprintf("path %s", path))
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
			r.log.V(1).Info("skipping file, failed to parse", "path", fullPath, "error", err)
			continue
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
		newProducer := producerForResource(res, producer)
		if existing, err := r.GetById(id); err == nil {
			existingProducer := r.provenance[idKey]
			if existingProducer == "" {
				existingProducer = producerForResource(existing, "existing resources")
			}
			_ = r.Remove(existing.CurId())
			r.warnings = append(r.warnings, duplicateWarning(id.String(), existingProducer, newProducer, source))
			r.log.V(1).Info("replacing duplicate resource", "id", id)
		}
		if err := r.Append(res); err != nil {
			return err
		}
		r.provenance[idKey] = newProducer
	}
	return nil
}

// Warnings returns non-fatal issues encountered while building the resource set.
func (r *Render) Warnings() []error {
	return append([]error(nil), r.warnings...)
}

// AbsorbAll merges resources into the render, replacing duplicates and recording warnings.
func (r *Render) AbsorbAll(src resmap.ResMap) error {
	return r.absorbResMap("expanded resources", "expanded resources", src)
}

// AddPaths recursively loads resources from a directory and all subdirectories.
// Each subdirectory is processed independently -- directories with a
// kustomization file are processed as kustomize bases; directories without one
// have their .yaml/.yml files loaded as raw manifests.
// When a directory is processed as a kustomize base, its subdirectories are
// not recursed into because kustomize already handles resource loading.
func (r *Render) AddPaths(fSys filesys.FileSystem, root string) error {
	return r.AddPathsWithProducer(fSys, root, fmt.Sprintf("path %s", root))
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
		r.provenance[res.CurId().String()] = producerForResource(res, producer)
	}
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

func producerForResource(res *resource.Resource, fallback string) string {
	labels := res.GetLabels()
	if name := labels["helm.toolkit.fluxcd.io/name"]; name != "" {
		ns := labels["helm.toolkit.fluxcd.io/namespace"]
		if ns == "" {
			ns = res.GetNamespace()
		}
		if ns != "" {
			return fmt.Sprintf("HelmRelease %s/%s", ns, name)
		}
		return fmt.Sprintf("HelmRelease %s", name)
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
func (r *Render) ApplyNamespaceToNew(count int, namespace string) {
	for _, res := range r.Resources()[count:] {
		oldID := res.CurId().String()
		if !res.GetGvk().IsClusterScoped() && res.GetNamespace() == "" {
			_ = res.SetNamespace(namespace)
			if producer, ok := r.provenance[oldID]; ok {
				delete(r.provenance, oldID)
				r.provenance[res.CurId().String()] = producer
			}
		}
	}
}

// AsJSON returns the rendered resources as a JSON array.
func (r *Render) AsJSON() ([]byte, error) {
	resources := r.Resources()
	buf := make([]byte, 0, len(resources)*256)
	buf = append(buf, '[')
	for i, res := range resources {
		if i > 0 {
			buf = append(buf, ',')
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		data, err := json.Marshal(m)
		if err != nil {
			continue
		}
		buf = append(buf, data...)
	}
	buf = append(buf, ']')
	return buf, nil
}
