package preview

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/tobiash/flux-manifest-preview/pkg/expander"
	"github.com/tobiash/flux-manifest-preview/pkg/expander/fluxks"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestRenderGraphBuildContexts(t *testing.T) {
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/repo/apps"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/repo/apps/cm.yaml", []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n  namespace: original\ndata:\n  value: ${VALUE}\n")); err != nil {
		t.Fatal(err)
	}
	g := fluxRenderGraph{loader: repoLoader{preview: &Preview{}, root: "/repo", fs: fs, log: logr.Discard(), render: render.NewDefaultRender(logr.Discard())}}
	a := expander.DiscoveredPath{Path: "apps", Producer: "Kustomization flux/a", Namespace: "a", Substitutions: map[string]string{"VALUE": "a"}}
	b := expander.DiscoveredPath{Path: "apps", Producer: "Kustomization flux/b", Namespace: "b", Substitutions: map[string]string{"VALUE": "b"}}
	for _, changed := range []expander.DiscoveredPath{
		{Path: "apps", Producer: "other", Namespace: a.Namespace, Substitutions: a.Substitutions},
		{Path: "apps", Producer: a.Producer, Namespace: "other", Substitutions: a.Substitutions},
		{Path: "apps", Producer: a.Producer, Namespace: a.Namespace, Substitutions: b.Substitutions},
	} {
		if g.pathKey(a) == g.pathKey(changed) {
			t.Errorf("pathKey ignores build context: %+v", changed)
		}
	}
	runner := expander.DiscoveryRunner{PathKey: g.pathKey, RenderPath: g.renderPath}
	if _, err := runner.Run(context.Background(), g.loader.render, []expander.DiscoveredPath{a, b}); err != nil {
		t.Fatal(err)
	}
	if got := g.loader.render.Size(); got != 2 {
		t.Fatalf("render size = %d, want 2 independent builds", got)
	}
	for _, res := range g.loader.render.Resources() {
		obj, err := res.Map()
		if err != nil {
			t.Fatal(err)
		}
		ns := res.GetNamespace()
		if ns != "a" && ns != "b" {
			t.Errorf("namespace = %q, want a or b", ns)
		}
		if got := obj["data"].(map[string]any)["value"]; got != ns {
			t.Errorf("substitution = %v, want %s", got, ns)
		}
		if got := g.loader.render.ProvenanceForID(res.CurId()).String(); got != "Kustomization flux/"+ns {
			t.Errorf("provenance = %q", got)
		}
	}
}

func TestRenderGraphBootstrapAndAliasesThroughPreview(t *testing.T) {
	for _, self := range []bool{false, true} {
		t.Run(fmt.Sprintf("self=%t", self), func(t *testing.T) {
			dir := t.TempDir()
			writePreviewFile(t, dir, "apps/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")
			if self {
				writePreviewFile(t, dir, "apps/ks.yaml", "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: self\n  namespace: flux-system\nspec:\n  path: ./apps\n")
			}
			p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"apps", "./apps", "apps/../apps"}, false), WithFluxKS())
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := p.Test(context.Background(), dir, &out); err != nil {
				t.Fatalf("Test(bootstrap/aliases) = %v, output %s", err, &out)
			}
			run, err := p.RunDiff(context.Background(), DiffRunOptions{LeftPath: dir, RightPath: dir})
			if err != nil {
				t.Fatalf("RunDiff(bootstrap/aliases) = %v", err)
			}
			if !run.Complete {
				t.Fatal("RunDiff(bootstrap/aliases) incomplete")
			}
		})
	}
}

func TestRenderGraphConflictingProducersRemainIncomplete(t *testing.T) {
	dir := t.TempDir()
	writePreviewFile(t, dir, "apps/cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")
	for _, name := range []string{"first", "second"} {
		writePreviewFile(t, dir, "apps/"+name+".yaml", fmt.Sprintf("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  path: apps\n", name))
	}
	p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"apps"}, false), WithFluxKS())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Test(context.Background(), dir, &bytes.Buffer{}); err == nil {
		t.Fatal("Test(conflicting producers) succeeded, want incomplete")
	}
}

func TestRenderGraphDistinctContextsThroughPreview(t *testing.T) {
	for _, namespaces := range []bool{false, true} {
		t.Run(fmt.Sprintf("namespaces=%t", namespaces), func(t *testing.T) {
			dir := t.TempDir()
			resourceName := "${NAME}"
			if namespaces {
				resourceName = "app"
			}
			writePreviewFile(t, dir, "apps/cm.yaml", fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  value: ${NAME}\n", resourceName))
			for _, name := range []string{"first", "second"} {
				namespace := ""
				if namespaces {
					namespace = "  targetNamespace: " + name + "\n"
				}
				writePreviewFile(t, dir, "root/"+name+".yaml", fmt.Sprintf("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  path: apps\n%s  postBuild:\n    substitute:\n      NAME: %s\n", name, namespace, name))
			}
			p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"root"}, false), WithFluxKS())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Test(context.Background(), dir, &bytes.Buffer{}); err != nil {
				t.Fatalf("Test(distinct contexts) = %v", err)
			}
			loaded, err := p.loadRepo(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, res := range loaded[""].render.Resources() {
				if res.GetKind() != "ConfigMap" {
					continue
				}
				count++
				identity := res.GetName()
				if namespaces {
					identity = res.GetNamespace()
				}
				if identity != "first" && identity != "second" {
					t.Errorf("ConfigMap identity = %q, want first or second", identity)
				}
				if got := loaded[""].render.ProvenanceForID(res.CurId()).String(); got != "Kustomization flux-system/"+identity {
					t.Errorf("ConfigMap provenance = %q", got)
				}
			}
			if count != 2 {
				t.Errorf("rendered ConfigMaps = %d, want 2", count)
			}
		})
	}
}

func TestRenderGraphRawRootWithToolConfig(t *testing.T) {
	dir := t.TempDir()
	writePreviewFile(t, dir, ".fmp.yaml", "paths:\n- .\nsort: true\n")
	writePreviewFile(t, dir, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\n")
	p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"."}, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Test(context.Background(), dir, &bytes.Buffer{}); err != nil {
		t.Fatalf("Test(raw root with tool config) = %v", err)
	}
}

func TestRenderGraphLateSourceResolution(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			dir, source := t.TempDir(), t.TempDir()
			writePreviewFile(t, source, "cm.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: late-app\n")
			writePreviewFile(t, dir, "root/local.yaml", fmt.Sprintf("apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: local\n  namespace: flux-system\nspec:\n  url: %s\n", dir))
			writePreviewFile(t, dir, "root/sources.yaml", "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: sources\n  namespace: flux-system\nspec:\n  path: sources\n  sourceRef:\n    kind: GitRepository\n    name: local\n")
			writePreviewFile(t, dir, "root/app.yaml", "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: app\n  namespace: flux-system\nspec:\n  path: .\n  sourceRef:\n    kind: GitRepository\n    name: late\n")
			name := "late"
			if missing {
				name = "unrelated"
			}
			writePreviewFile(t, dir, "sources/repo.yaml", fmt.Sprintf("apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: %s\n  namespace: flux-system\nspec:\n  url: %s\n", name, source))
			p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"root"}, false), WithFluxKS(), WithGitRepo())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(p.gitRepoExpander.Cleanup)
			var out bytes.Buffer
			err = p.Test(context.Background(), dir, &out)
			if missing {
				if err == nil || !strings.Contains(err.Error(), "unresolved GitRepository flux-system/late") {
					t.Fatalf("Test(missing source) = %v, want unresolved source failure", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Test(late source) = %v, output %s", err, &out)
			}
			out.Reset()
			if err := p.Render(context.Background(), dir, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "name: late-app") {
				t.Fatalf("Render(late source) omitted app: %s", &out)
			}
		})
	}
}

func TestRenderGraphOmittedPathUsesSourceRoot(t *testing.T) {
	dir := t.TempDir()
	writePreviewFile(t, dir, "ks.yaml", "apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: self\n  namespace: flux-system\nspec:\n  prune: true\n")
	p, err := New(WithLogger(logr.Discard()), WithPaths([]string{"."}, false), WithFluxKS())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Test(context.Background(), dir, &bytes.Buffer{}); err != nil {
		t.Fatalf("Test(omitted path) = %v", err)
	}
}

func TestRenderGraphSelfReferenceTerminates(t *testing.T) {
	fs := filesys.MakeFsInMemory()
	if err := fs.MkdirAll("/repo/apps"); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile("/repo/apps/ks.yaml", []byte("apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: self\n  namespace: flux-system\nspec:\n  path: apps\n")); err != nil {
		t.Fatal(err)
	}
	g := fluxRenderGraph{loader: repoLoader{preview: &Preview{}, root: "/repo", fs: fs, log: logr.Discard(), render: render.NewDefaultRender(logr.Discard())}}
	registry := expander.NewRegistry(logr.Discard())
	registry.Register(fluxks.NewExpander(logr.Discard()))
	runner := expander.DiscoveryRunner{Expanders: registry, PathKey: g.pathKey, RenderPath: g.renderPath, MaxIterations: 3}
	if _, err := runner.Run(context.Background(), g.loader.render, []expander.DiscoveredPath{{Path: "apps", Producer: "path apps"}}); err != nil {
		t.Fatalf("self-reference failed to terminate: %v", err)
	}
}

func TestRenderGraphMissingDiscoveredPathFails(t *testing.T) {
	g := fluxRenderGraph{loader: repoLoader{root: "/repo", fs: filesys.MakeFsInMemory()}}
	if err := g.renderPath(expander.DiscoveredPath{Path: "missing", Producer: "Kustomization flux/apps"}); err == nil {
		t.Fatal("renderPath(missing discovered path) succeeded, want error")
	}
}
