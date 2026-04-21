package helm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	helmcli "helm.sh/helm/v4/pkg/cli"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

func TestRenderChart_SetsReleaseNamespaceWithoutNamespacingClusterScopedResources(t *testing.T) {
	chartDir := t.TempDir()
	writeChartFile(t, chartDir, "Chart.yaml", "apiVersion: v2\nname: test\nversion: 0.1.0\n")
	writeChartFile(t, chartDir, "templates/resources.yaml", `apiVersion: v1
kind: ConfigMap
metadata:
  name: namespaced
  namespace: {{ .Release.Namespace }}
data:
  value: ok
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: test-webhook
webhooks:
- name: test.example.com
  sideEffects: None
  admissionReviewVersions: ["v1"]
  clientConfig:
    service:
      name: webhook
      namespace: {{ .Release.Namespace }}
      path: /validate
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    operations: ["CREATE"]
    resources: ["pods"]
---
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: test-snapclass
driver: rook-ceph.rbd.csi.ceph.com
deletionPolicy: Delete
parameters:
  clusterID: {{ .Release.Namespace }}
`)

	runner := NewRunner(helmcli.New(), logr.Discard())
	resources, err := runner.renderChart(context.Background(), &RenderTask{
		localChartPath: chartDir,
		chart:          "./chart",
		releaseName:    "demo",
		namespace:      "demo-ns",
	})
	if err != nil {
		t.Fatalf("renderChart() error = %v", err)
	}

	configMap, err := resources.GetById(resid.NewResIdWithNamespace(resid.NewGvk("", "v1", "ConfigMap"), "namespaced", "demo-ns"))
	if err != nil {
		t.Fatalf("GetById(configmap) error = %v", err)
	}
	if got := configMap.GetNamespace(); got != "demo-ns" {
		t.Fatalf("ConfigMap namespace = %q, want demo-ns", got)
	}

	webhook, err := resources.GetById(resid.NewResId(resid.NewGvk("admissionregistration.k8s.io", "v1", "ValidatingWebhookConfiguration"), "test-webhook"))
	if err != nil {
		t.Fatalf("GetById(webhook) error = %v", err)
	}
	webhookMap, err := webhook.Map()
	if err != nil {
		t.Fatalf("webhook.Map() error = %v", err)
	}
	serviceNamespace := webhookMap["webhooks"].([]any)[0].(map[string]any)["clientConfig"].(map[string]any)["service"].(map[string]any)["namespace"]
	if serviceNamespace != "demo-ns" {
		t.Fatalf("webhook service namespace = %v, want demo-ns", serviceNamespace)
	}
	if _, found := webhookMap["metadata"].(map[string]any)["namespace"]; found {
		t.Fatalf("expected ValidatingWebhookConfiguration to remain cluster-scoped, got metadata.namespace=%v", webhookMap["metadata"].(map[string]any)["namespace"])
	}

	snapClass, err := resources.GetById(resid.NewResId(resid.NewGvk("snapshot.storage.k8s.io", "v1", "VolumeSnapshotClass"), "test-snapclass"))
	if err != nil {
		t.Fatalf("GetById(snapshotclass) error = %v", err)
	}
	snapMap, err := snapClass.Map()
	if err != nil {
		t.Fatalf("snapClass.Map() error = %v", err)
	}
	if _, found := snapMap["metadata"].(map[string]any)["namespace"]; found {
		t.Fatalf("expected VolumeSnapshotClass to remain cluster-scoped, got metadata.namespace=%v", snapMap["metadata"].(map[string]any)["namespace"])
	}
	if clusterID := snapMap["parameters"].(map[string]any)["clusterID"]; clusterID != "demo-ns" {
		t.Fatalf("snapshot class clusterID = %v, want demo-ns", clusterID)
	}
}

func writeChartFile(t *testing.T, root, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", relPath, err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", relPath, err)
	}
}
