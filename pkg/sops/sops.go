package sops

import (
	"fmt"
	"os"
	"os/exec"

	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

const (
	SopsField = "sops"
	SecretGVK = "Secret"
)

func IsSOPSContainer(m map[string]any) bool {
	_, ok := m[SopsField]
	return ok
}

func DecryptResources(rm resmap.ResMap) error {
	for _, res := range rm.Resources() {
		if res.GetKind() != SecretGVK {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		if !IsSOPSContainer(m) {
			continue
		}

		yamlData, err := res.AsYAML()
		if err != nil {
			return fmt.Errorf("marshaling SOPS secret %s: %w", res.CurId(), err)
		}

		decrypted, err := decryptYAML(yamlData)
		if err != nil {
			return fmt.Errorf("decrypting SOPS secret %s: %w", res.CurId(), err)
		}

		factory := resmap.NewFactory(resource.NewFactory(nil))
		decryptedMap, err := factory.NewResMapFromBytes(decrypted)
		if err != nil {
			return fmt.Errorf("parsing decrypted SOPS secret %s: %w", res.CurId(), err)
		}

		decryptedResources := decryptedMap.Resources()
		if len(decryptedResources) == 0 {
			continue
		}

		decryptedRes := decryptedResources[0]
		origId := res.CurId()
		_ = rm.Remove(origId)
		if err := rm.Append(decryptedRes); err != nil {
			return fmt.Errorf("replacing decrypted SOPS secret %s: %w", origId, err)
		}
	}
	return nil
}

func decryptYAML(data []byte) ([]byte, error) {
	f, err := os.CreateTemp("", "fmp-sops-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("closing temp file: %w", err)
	}

	cmd := exec.Command("sops", "-d", f.Name())
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("sops decrypt failed: %w (stderr: %s)", err, string(ee.Stderr))
		}
		return nil, fmt.Errorf("sops decrypt failed: %w", err)
	}
	return out, nil
}

func HasSOPSResources(rm resmap.ResMap) bool {
	for _, res := range rm.Resources() {
		if res.GetKind() != SecretGVK {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		if IsSOPSContainer(m) {
			return true
		}
	}
	return false
}

func SOPSResourceIDs(rm resmap.ResMap) []resid.ResId {
	var ids []resid.ResId
	for _, res := range rm.Resources() {
		if res.GetKind() != SecretGVK {
			continue
		}
		m, err := res.Map()
		if err != nil {
			continue
		}
		if IsSOPSContainer(m) {
			ids = append(ids, res.CurId())
		}
	}
	return ids
}
