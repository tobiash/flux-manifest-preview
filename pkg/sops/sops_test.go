package sops

import (
	"testing"

	"sigs.k8s.io/kustomize/api/hasher"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
)

func TestIsSOPSContainer(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		want bool
	}{
		{
			name: "has sops key",
			m: map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data":       map[string]any{"key": "val"},
				"sops":       map[string]any{"mac": "abc"},
			},
			want: true,
		},
		{
			name: "no sops key",
			m: map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"data":       map[string]any{"key": "val"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSOPSContainer(tt.m); got != tt.want {
				t.Errorf("IsSOPSContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasSOPSResources(t *testing.T) {
	rm := makeSOPSResMap(t, `apiVersion: v1
kind: Secret
metadata:
  name: sops-secret
  namespace: default
data:
  key: QUJD
sops:
  mac: "encrypted-mac"
  lastmodified: "2024-01-01T00:00:00Z"
`)

	if !HasSOPSResources(rm) {
		t.Error("expected HasSOPSResources to return true")
	}

	ids := SOPSResourceIDs(rm)
	if len(ids) != 1 {
		t.Fatalf("expected 1 SOPS resource ID, got %d", len(ids))
	}
	if ids[0].Name != "sops-secret" {
		t.Errorf("expected resource name 'sops-secret', got %q", ids[0].Name)
	}
}

func TestHasSOPSResources_NoSOPS(t *testing.T) {
	rm := makeResMap(t, `apiVersion: v1
kind: Secret
metadata:
  name: plain-secret
  namespace: default
data:
  key: QUJD
`)

	if HasSOPSResources(rm) {
		t.Error("expected HasSOPSResources to return false for plain secret")
	}
}

func makeResMap(t *testing.T, y string) resmap.ResMap {
	t.Helper()
	factory := resmap.NewFactory(resource.NewFactory(&hasher.Hasher{}))
	rm, err := factory.NewResMapFromBytes([]byte(y))
	if err != nil {
		t.Fatalf("failed to create resmap: %v", err)
	}
	return rm
}

func makeSOPSResMap(t *testing.T, y string) resmap.ResMap {
	t.Helper()
	return makeResMap(t, y)
}
