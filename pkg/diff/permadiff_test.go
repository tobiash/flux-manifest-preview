package diff

import (
	"bytes"
	"strings"
	"testing"
)

func TestDetectPermadiffs_TLSecretDataDiff(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: LS0tLS1CRUdJTi0x
  tls.key: LS0tLS1CRUdJTi0y
  ca.crt: LS0tLS1CRUdJTi0z
`)
	b := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: QUJDREVG
  tls.key: R0hJSks=
  ca.crt: TU5PUFE=
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	if len(diffs) == 0 {
		t.Fatal("expected permadiffs to be detected")
	}

	foundTLSKey := false
	for _, d := range diffs {
		if len(d.FieldPath) >= 2 && d.FieldPath[0] == "data" && d.FieldPath[1] == "tls.key" {
			foundTLSKey = true
		}
	}
	if !foundTLSKey {
		t.Errorf("expected diff on data/tls.key, got diffs: %+v", diffs)
	}
}

func TestDetectPermadiffs_IdenticalResources(t *testing.T) {
	yaml := `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
  namespace: default
data:
  key: same
`
	a := makeRender(t, yaml)
	b := makeRender(t, yaml)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	if len(diffs) != 0 {
		t.Errorf("expected no permadiffs for identical resources, got: %+v", diffs)
	}
}

func TestDetectPermadiffs_SOPSMetadataDiff(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: sops-secret
  namespace: default
data:
  key: QUJD
sops:
  mac: "mac-value-1"
  lastmodified: "2024-01-01T00:00:00Z"
`)
	b := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: sops-secret
  namespace: default
data:
  key: QUJD
sops:
  mac: "mac-value-2"
  lastmodified: "2024-01-02T00:00:00Z"
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	if len(diffs) == 0 {
		t.Fatal("expected permadiffs from sops metadata change")
	}

	foundSOPS := false
	for _, d := range diffs {
		if len(d.FieldPath) >= 1 && d.FieldPath[0] == "sops" {
			foundSOPS = true
		}
	}
	if !foundSOPS {
		t.Errorf("expected diff on sops field, got diffs: %+v", diffs)
	}
}

func TestGenerateFilterConfig_NoDiffs(t *testing.T) {
	config, err := GenerateFilterConfig(nil)
	if err != nil {
		t.Fatalf("GenerateFilterConfig() error = %v", err)
	}
	if !strings.Contains(string(config), "No permadiffs") {
		t.Errorf("expected 'no permadiffs' message, got: %s", string(config))
	}
}

func TestGenerateFilterConfig_TLSecretDiffs(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: LS0tMQ==
  tls.key: LS0tMg==
  ca.crt: LS0tMw==
`)
	b := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: QUJD
  tls.key: REVG
  ca.crt: R0hJ
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	config, err := GenerateFilterConfig(diffs)
	if err != nil {
		t.Fatalf("GenerateFilterConfig() error = %v", err)
	}

	configStr := string(config)
	t.Logf("Generated config:\n%s", configStr)

	if !strings.Contains(configStr, "FieldNormalizer") {
		t.Error("expected config to contain FieldNormalizer")
	}
	if !strings.Contains(configStr, "Secret") {
		t.Error("expected config to match Secret kind")
	}
	if !strings.Contains(configStr, "auto-generated") {
		t.Error("expected config to contain auto-generated placeholder")
	}
}

func TestWritePermadiffConfig(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
data:
  tls.crt: LS0tMQ==
  tls.key: LS0tMg==
`)
	b := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
data:
  tls.crt: QUJD
  tls.key: REVG
`)

	var buf bytes.Buffer
	if err := WritePermadiffConfig(a, b, &buf); err != nil {
		t.Fatalf("WritePermadiffConfig() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "FieldNormalizer") {
		t.Errorf("expected output to contain FieldNormalizer, got:\n%s", output)
	}
}

func TestDetectPermadiffs_WebhookArrayDiff(t *testing.T) {
	a := makeRender(t, `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: my-webhook
  namespace: default
webhooks:
  - name: webhook1.example.com
    clientConfig:
      caBundle: LS0tLS1CRUdJTi0x
      url: https://webhook1.example.com
    sideEffects: None
  - name: webhook2.example.com
    clientConfig:
      caBundle: LS0tLS1CRUdJTi0y
      url: https://webhook2.example.com
    sideEffects: None
`)
	b := makeRender(t, `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: my-webhook
  namespace: default
webhooks:
  - name: webhook1.example.com
    clientConfig:
      caBundle: QUJDREVG
      url: https://webhook1.example.com
    sideEffects: None
  - name: webhook2.example.com
    clientConfig:
      caBundle: R0hJSks=
      url: https://webhook2.example.com
    sideEffects: None
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	if len(diffs) == 0 {
		t.Fatal("expected permadiffs to be detected in webhook caBundle")
	}

	for _, d := range diffs {
		t.Logf("diff: %v", d.FieldPath)
	}

	foundCaBundle := false
	for _, d := range diffs {
		if len(d.FieldPath) >= 4 && d.FieldPath[2] == "clientConfig" && d.FieldPath[3] == "caBundle" {
			foundCaBundle = true
		}
	}
	if !foundCaBundle {
		t.Errorf("expected diff on webhooks[*].clientConfig.caBundle, got diffs: %+v", diffs)
	}

	rules := GroupDiffsToRules(diffs)
	if len(rules) == 0 {
		t.Fatal("expected rules to be generated")
	}

	rule := rules[0]
	foundStar := false
	for _, fp := range rule.FieldPaths {
		for _, p := range fp.Path {
			if p == "[*]" {
				foundStar = true
			}
		}
	}
	if !foundStar {
		t.Errorf("expected [*] in rule field paths, got: %+v", rules)
	}

	config, err := GenerateFilterConfig(diffs)
	if err != nil {
		t.Fatalf("GenerateFilterConfig() error = %v", err)
	}
	configStr := string(config)
	t.Logf("Generated config:\n%s", configStr)

	if !strings.Contains(configStr, "ValidatingWebhookConfiguration") {
		t.Error("expected config to match ValidatingWebhookConfiguration")
	}
	if !strings.Contains(configStr, "[*]") {
		t.Error("expected config to contain [*] for array wildcard")
	}
}

func TestDetectPermadiffs_APIServiceCaBundle(t *testing.T) {
	a := makeRender(t, `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1beta1.custom.metrics.k8s.io
  namespace: default
spec:
  caBundle: LS0tLS1CRUdJTi0x
  service:
    name: custom-metrics
    namespace: monitoring
`)
	b := makeRender(t, `apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1beta1.custom.metrics.k8s.io
  namespace: default
spec:
  caBundle: QUJDREVG
  service:
    name: custom-metrics
    namespace: monitoring
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	if len(diffs) == 0 {
		t.Fatal("expected permadiffs to be detected in APIService caBundle")
	}

	foundCaBundle := false
	for _, d := range diffs {
		if len(d.FieldPath) >= 2 && d.FieldPath[0] == "spec" && d.FieldPath[1] == "caBundle" {
			foundCaBundle = true
		}
	}
	if !foundCaBundle {
		t.Errorf("expected diff on spec/caBundle, got diffs: %+v", diffs)
	}

	config, err := GenerateFilterConfig(diffs)
	if err != nil {
		t.Fatalf("GenerateFilterConfig() error = %v", err)
	}
	configStr := string(config)
	t.Logf("Generated config:\n%s", configStr)

	if !strings.Contains(configStr, "APIService") {
		t.Error("expected config to match APIService kind")
	}
	if !strings.Contains(configStr, "apiregistration.k8s.io") {
		t.Error("expected config to match apiregistration.k8s.io group")
	}
}

func TestGenerateFilterConfig_SecretDataSpecificKeys(t *testing.T) {
	a := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
data:
  tls.crt: LS0tMQ==
  tls.key: LS0tMg==
  ca.crt: LS0tMw==
`)
	b := makeRender(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-cert
  namespace: default
data:
  tls.crt: QUJD
  tls.key: REVG
  ca.crt: R0hJ
`)

	diffs, err := DetectPermadiffs(a, b)
	if err != nil {
		t.Fatalf("DetectPermadiffs() error = %v", err)
	}

	config, err := GenerateFilterConfig(diffs)
	if err != nil {
		t.Fatalf("GenerateFilterConfig() error = %v", err)
	}

	configStr := string(config)
	t.Logf("Generated config:\n%s", configStr)

	if strings.Contains(configStr, "- data\n    - \"*\"") {
		t.Error("should not use data.* glob for Secret data fields, expected specific keys")
	}
	if !strings.Contains(configStr, "tls.crt") {
		t.Error("expected specific key tls.crt in config")
	}
	if !strings.Contains(configStr, "tls.key") {
		t.Error("expected specific key tls.key in config")
	}
	if !strings.Contains(configStr, "ca.crt") {
		t.Error("expected specific key ca.crt in config")
	}
}
