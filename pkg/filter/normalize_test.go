package filter

import (
	"encoding/base64"
	"testing"

	"sigs.k8s.io/kustomize/kyaml/yaml"
)

func TestFieldNormalizer_ReplaceSecretData(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-secret
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: LS0tLS1CRUdJTi0=
  tls.key: LS0tLS1CRUdJTi1Q
  ca.crt: LS0tLS1CRUdJTi0=
stringData:
  plain: sometext
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "tls.crt"}, Action: ActionReplace, Placeholder: "<<auto>>"},
			{Path: []string{"data", "tls.key"}, Action: ActionReplace, Placeholder: "<<auto>>"},
			{Path: []string{"data", "ca.crt"}, Action: ActionReplace, Placeholder: "<<auto>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()

	expected := base64.StdEncoding.EncodeToString([]byte("<<auto>>"))
	if !contains(got, expected) {
		t.Errorf("expected data values to be replaced with base64-encoded placeholder %q, got:\n%s", expected, got)
	}
	if contains(got, "LS0tLS1CRUdJTi0=") {
		t.Error("expected original ca.crt value to be replaced")
	}
	if contains(got, "LS0tLS1CRUdJTi1Q") {
		t.Error("expected original tls.key value to be replaced")
	}
	if !contains(got, "sometext") {
		t.Error("expected stringData.plain to be preserved")
	}
}

func TestFieldNormalizer_RemoveField(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: sops-secret
  namespace: default
data:
  key: dmFsdWU=
sops:
  mac: "some-mac-value"
  lastmodified: "2024-01-01T00:00:00Z"
  enc: "encrypted-data"
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret"},
		FieldPaths: []FieldPath{
			{Path: []string{"sops"}, Action: ActionRemove},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if contains(got, "some-mac-value") {
		t.Error("expected sops block to be removed")
	}
	if !contains(got, "dmFsdWU=") {
		t.Error("expected data.key to be preserved")
	}
}

func TestFieldNormalizer_GlobPattern(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: tls-secret
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: AAAA
  tls.key: BBBB
  ca.crt: CCCC
  other: DDDD
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "tls.*"}, Action: ActionReplace, Placeholder: "<<tls>>"},
			{Path: []string{"data", "ca.*"}, Action: ActionReplace, Placeholder: "<<ca>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()

	tlsPlaceholder := base64.StdEncoding.EncodeToString([]byte("<<tls>>"))
	caPlaceholder := base64.StdEncoding.EncodeToString([]byte("<<ca>>"))

	if !contains(got, tlsPlaceholder) {
		t.Errorf("expected tls fields to be replaced with %q, got:\n%s", tlsPlaceholder, got)
	}
	if !contains(got, caPlaceholder) {
		t.Errorf("expected ca fields to be replaced with %q, got:\n%s", caPlaceholder, got)
	}
	if !contains(got, "DDDD") {
		t.Error("expected 'other' field to be preserved (doesn't match tls.* or ca.*)")
	}
}

func TestFieldNormalizer_WildcardAllData(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: enc-secret
data:
  key1: AAAA
  key2: BBBB
  key3: CCCC
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "*"}, Action: ActionReplace, Placeholder: "<<enc>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	placeholder := base64.StdEncoding.EncodeToString([]byte("<<enc>>"))
	count := 0
	for _, line := range splitLines(got) {
		if contains(line, placeholder) {
			count++
		}
	}
	if count != 3 {
		t.Errorf("expected 3 fields to be replaced, got %d in:\n%s", count, got)
	}
}

func TestFieldNormalizer_MatchByName(t *testing.T) {
	secret1 := parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: web-cert
  namespace: default
data:
  tls.crt: AAAA
  tls.key: BBBB
`)
	secret2 := parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: db-cred
  namespace: default
data:
  password: CCCC
`)

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret", Name: "web-*"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "tls.*"}, Action: ActionReplace, Placeholder: "<<auto>>"},
		},
	}

	result, err := fn.Filter([]*yaml.RNode{secret1, secret2})
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got0 := result[0].MustString()
	placeholder := base64.StdEncoding.EncodeToString([]byte("<<auto>>"))
	if !contains(got0, placeholder) {
		t.Errorf("expected web-cert data to be normalized, got:\n%s", got0)
	}

	got1 := result[1].MustString()
	if contains(got1, placeholder) {
		t.Errorf("expected db-cred to be untouched (name doesn't match web-*), got:\n%s", got1)
	}
	if !contains(got1, "CCCC") {
		t.Error("expected db-cred password to be preserved")
	}
}

func TestFieldNormalizer_MatchGroupVersion(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-deploy
  namespace: default
spec:
  template:
    metadata:
      annotations:
        random: value1
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Group: "apps", Version: "v1", Kind: "Deployment"},
		FieldPaths: []FieldPath{
			{Path: []string{"spec", "template", "metadata", "annotations", "random"}, Action: ActionReplace, Placeholder: "<<stable>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if !contains(got, "<<stable>>") {
		t.Errorf("expected annotation to be replaced, got:\n%s", got)
	}
}

func TestFieldNormalizer_NoMatchLeavesUntouched(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  key: value
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "*"}, Action: ActionReplace, Placeholder: "<<auto>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if !contains(got, "key: value") {
		t.Errorf("expected ConfigMap to be untouched, got:\n%s", got)
	}
}

func TestFieldNormalizer_DefaultPlaceholder(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  token: abc123
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "ConfigMap"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "token"}, Action: ActionReplace},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if !contains(got, "<<auto-generated>>") {
		t.Errorf("expected default placeholder, got:\n%s", got)
	}
}

func TestFieldNormalizer_HasAnnotation(t *testing.T) {
	sopsSecret := parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: sops-secret
  annotations:
    kustomize.config.k8s.io/sops: "true"
data:
  key: dmFsdWU=
`)
	plainSecret := parseRNode(t, `apiVersion: v1
kind: Secret
metadata:
  name: plain-secret
data:
  key: dmFsdWU=
`)

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "Secret", HasAnnotation: "kustomize.config.k8s.io/sops"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "*"}, Action: ActionReplace, Placeholder: "<<enc>>"},
		},
	}

	result, err := fn.Filter([]*yaml.RNode{sopsSecret, plainSecret})
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got0 := result[0].MustString()
	placeholder := base64.StdEncoding.EncodeToString([]byte("<<enc>>"))
	if !contains(got0, placeholder) {
		t.Errorf("expected sops-secret data to be normalized, got:\n%s", got0)
	}

	got1 := result[1].MustString()
	if contains(got1, placeholder) {
		t.Errorf("expected plain-secret to be untouched (no sops annotation), got:\n%s", got1)
	}
}

func TestFieldNormalizer_ArrayWildcard(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: my-webhook
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
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{
			Group: "admissionregistration.k8s.io",
			Kind:  "ValidatingWebhookConfiguration",
		},
		FieldPaths: []FieldPath{
			{Path: []string{"webhooks", "[*]", "clientConfig", "caBundle"}, Action: ActionReplace, Placeholder: "<<auto>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()

	if !contains(got, "<<auto>>") {
		t.Errorf("expected caBundle values to be replaced with <<auto>>, got:\n%s", got)
	}
	if contains(got, "LS0tLS1CRUdJTi0x") {
		t.Errorf("expected original caBundle values to be replaced, got:\n%s", got)
	}
	if !contains(got, "webhook1.example.com") {
		t.Error("expected webhook names to be preserved")
	}
	if !contains(got, "https://webhook1.example.com") {
		t.Error("expected webhook URLs to be preserved")
	}
	if !contains(got, "None") {
		t.Error("expected sideEffects to be preserved")
	}
}

func TestFieldNormalizer_NonSecretDataNotBase64(t *testing.T) {
	input := []*yaml.RNode{
		parseRNode(t, `apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  cert: -----BEGIN CERTIFICATE-----
  key: -----BEGIN KEY-----
`),
	}

	fn := FieldNormalizer{
		Match: MatchCriteria{Kind: "ConfigMap"},
		FieldPaths: []FieldPath{
			{Path: []string{"data", "cert"}, Action: ActionReplace, Placeholder: "<<norm>>"},
			{Path: []string{"data", "key"}, Action: ActionReplace, Placeholder: "<<norm>>"},
		},
	}

	result, err := fn.Filter(input)
	if err != nil {
		t.Fatalf("Filter() error = %v", err)
	}

	got := result[0].MustString()
	if !contains(got, "<<norm>>") {
		t.Errorf("ConfigMap data should use raw placeholder, got:\n%s", got)
	}
	b64Placeholder := base64.StdEncoding.EncodeToString([]byte("<<norm>>"))
	if contains(got, b64Placeholder) {
		t.Errorf("ConfigMap data should NOT be base64-encoded, got:\n%s", got)
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
