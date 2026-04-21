package helm

import (
	"bytes"
	"testing"

	v2 "github.com/fluxcd/helm-controller/api/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPostRendererOriginLabels(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: default
data:
  key: value
`
	renderer := newPostRendererOriginLabels("myrelease", "mynamespace")

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "helm.toolkit.fluxcd.io/name: myrelease") {
		t.Errorf("expected origin name label, got:\n%s", result)
	}
	if !contains(result, "helm.toolkit.fluxcd.io/namespace: mynamespace") {
		t.Errorf("expected origin namespace label, got:\n%s", result)
	}
}

func TestPostRendererOriginLabels_MultipleResources(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: cm1
---
apiVersion: v1
kind: Secret
metadata:
  name: secret1
`
	renderer := newPostRendererOriginLabels("rel", "ns")
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	count := bytes.Count([]byte(result), []byte("helm.toolkit.fluxcd.io/name: rel"))
	if count != 2 {
		t.Errorf("expected 2 origin name labels, got %d", count)
	}
}

func TestPostRendererOriginLabels_PreservesNestedNamespaceFields(t *testing.T) {
	input := `apiVersion: admissionregistration.k8s.io/v1
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
      namespace: cert-manager
      path: /validate
  rules:
  - apiGroups: [""]
    apiVersions: ["v1"]
    operations: ["CREATE"]
    resources: ["pods"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: test-binding
  namespace: cert-manager
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: test
subjects:
- kind: ServiceAccount
  name: test-sa
  namespace: cert-manager
`
	renderer := newPostRendererOriginLabels("rel", "ns")

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "namespace: cert-manager") {
		t.Fatalf("expected nested namespace fields to be preserved, got:\n%s", result)
	}
	if contains(result, "namespace: null") {
		t.Fatalf("expected no nested null namespace fields, got:\n%s", result)
	}
}

func TestPostRendererCommonMetadata_Labels(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	cm := &v2.CommonMetadata{
		Labels:      map[string]string{"app": "test", "env": "prod"},
		Annotations: nil,
	}
	renderer := newPostRendererCommonMetadata(cm)

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "app: test") {
		t.Errorf("expected 'app: test' label, got:\n%s", result)
	}
	if !contains(result, "env: prod") {
		t.Errorf("expected 'env: prod' label, got:\n%s", result)
	}
}

func TestPostRendererCommonMetadata_NilBoth(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	cm := &v2.CommonMetadata{}
	renderer := newPostRendererCommonMetadata(cm)

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if out.String() != input {
		t.Errorf("expected unchanged output when commonMetadata is empty")
	}
}

func TestPostRendererCommonMetadata_SkipsNamelessDocs(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
---
apiVersion: ceph.rook.io/v1
kind: CephCluster
spec:
  dataDirHostPath: /var/lib/rook
`
	cm := &v2.CommonMetadata{Labels: map[string]string{"app": "test"}}
	renderer := newPostRendererCommonMetadata(cm)

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "app: test") {
		t.Fatalf("expected common metadata label on named object, got:\n%s", result)
	}
	if contains(result, "CephCluster") {
		t.Fatalf("expected nameless document to be skipped by post-render transform, got:\n%s", result)
	}
	if !contains(result, "name: test-cm") {
		t.Fatalf("expected named document to remain, got:\n%s", result)
	}
}

func TestPostRendererOriginLabels_SkipsNamelessDocs(t *testing.T) {
	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
---
apiVersion: ceph.rook.io/v1
kind: CephCluster
spec:
  dataDirHostPath: /var/lib/rook
`
	renderer := newPostRendererOriginLabels("rel", "ns")

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if !contains(result, "helm.toolkit.fluxcd.io/name: rel") {
		t.Fatalf("expected origin label on named object, got:\n%s", result)
	}
	if contains(result, "CephCluster") {
		t.Fatalf("expected nameless document to be skipped by post-render transform, got:\n%s", result)
	}
}

func TestPostRendererOriginLabels_DoesNotNamespaceClusterScopedResources(t *testing.T) {
	input := `apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: test-snapclass
driver: rook-ceph.rbd.csi.ceph.com
deletionPolicy: Delete
parameters:
  clusterID: rook-ceph
`
	renderer := newPostRendererOriginLabels("rel", "rook-ceph")

	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := out.String()
	if contains(result, "metadata:\n  name: test-snapclass\n  namespace:") {
		t.Fatalf("expected cluster-scoped metadata.namespace to stay absent, got:\n%s", result)
	}
	if !contains(result, "clusterID: rook-ceph") {
		t.Fatalf("expected snapshot class fields to be preserved, got:\n%s", result)
	}
}

func TestBuildPostRenderers_IncludesOriginLabels(t *testing.T) {
	hr := &v2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
	}
	renderer := buildPostRenderers(hr)
	if renderer == nil {
		t.Fatal("expected non-nil post-renderer (at minimum origin labels)")
	}

	input := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
`
	out, err := renderer.Run(bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !contains(out.String(), "helm.toolkit.fluxcd.io/name: test") {
		t.Error("expected origin name label in output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}
