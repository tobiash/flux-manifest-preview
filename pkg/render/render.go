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
}

// NewDefaultRender creates a Render with default kustomize options.

func NewDefaultRender(log logr.Logger) *Render {
	return &Render{
		ResMap:     resmap.New(),
		kustomizer: krusty.MakeKustomizer(krusty.MakeDefaultOptions()),
		log:        log,
	}
}

// AddKustomization runs kustomize on the given path and appends the results.

func (r *Render) AddKustomization(fSys filesys.FileSystem, path string) error {
	resmap, err := r.kustomizer.Run(fSys, path)
	if err != nil {
		return err
	}
	return r.AppendAll(resmap)
}

// AddPath loads resources from a directory path. If the directory contains a
// kustomization file (kustomization.yaml, kustomization.yml, or Kustomization),
// it is processed as a kustomize base. Otherwise all .yaml/.yml files in the
// directory are loaded as raw Kubernetes manifests.
func (r *Render) AddPath(fSys filesys.FileSystem, path string) error {
	if isKustomization(fSys, path) {
		return r.AddKustomization(fSys, path)
	}
	return r.addRawYAMLFiles(fSys, path)
}

func (r *Render) addRawYAMLFiles(fSys filesys.FileSystem, dir string) error {
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

		if err := r.AppendAll(resources); err != nil {
			return fmt.Errorf("appending resources from %s: %w", fullPath, err)
		}
	}

	return nil
}

// AddPaths recursively loads resources from a directory and all subdirectories.
// Each subdirectory is processed independently -- directories with a
// kustomization file are processed as kustomize bases; directories without one
// have their .yaml/.yml files loaded as raw manifests.
// When a directory is processed as a kustomize base, its subdirectories are
// not recursed into because kustomize already handles resource loading.
func (r *Render) AddPaths(fSys filesys.FileSystem, root string) error {
	isKust := isKustomization(fSys, root)
	if isKust {
		if err := r.AddKustomization(fSys, root); err != nil {
			return err
		}
	} else {
		if err := r.addRawYAMLFiles(fSys, root); err != nil {
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
			if err := r.AddPaths(fSys, sub); err != nil {
				return err
			}
		}
	}

	return nil
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

// FilterCRDs removes all CustomResourceDefinition resources from the render.
func (r *Render) FilterCRDs() {
	for _, res := range r.Resources() {
		if res.GetKind() == "CustomResourceDefinition" {
			r.Remove(res.CurId())
		}
	}
}

// ApplyNamespaceToNew sets the namespace on all namespace-scoped resources
// added after the given count. This is used to apply Flux Kustomization
// spec.targetNamespace to newly rendered resources.
func (r *Render) ApplyNamespaceToNew(count int, namespace string) {
	for _, res := range r.Resources()[count:] {
		if !res.GetGvk().IsClusterScoped() && res.GetNamespace() == "" {
			res.SetNamespace(namespace)
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