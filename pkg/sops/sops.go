package sops

import (
	"fmt"

	"github.com/getsops/sops/v3/decrypt"
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
		rm.Remove(origId)
		if err := rm.Append(decryptedRes); err != nil {
			return fmt.Errorf("replacing decrypted SOPS secret %s: %w", origId, err)
		}
	}
	return nil
}

func decryptYAML(data []byte) ([]byte, error) {
	decrypted, err := decrypt.Data(data, "yaml")
	if err != nil {
		return nil, fmt.Errorf("sops decrypt failed: %w", err)
	}
	return decrypted, nil
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
