/*
Copyright 2021 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package helm

import (
	"bytes"
	"fmt"

	"github.com/go-logr/logr"
	"sigs.k8s.io/kustomize/api/builtins" //nolint:staticcheck // api/builtins deprecated but no stable replacement yet
	kustypes "sigs.k8s.io/kustomize/api/types"

	v2 "github.com/fluxcd/helm-controller/api/v2"
)

// postRendererOriginLabels adds origin labels to all rendered resources.
type postRendererOriginLabels struct {
	name      string
	namespace string
}

func newPostRendererOriginLabels(name, namespace string) *postRendererOriginLabels {
	return &postRendererOriginLabels{name: name, namespace: namespace}
}

func (k *postRendererOriginLabels) Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error) {
	resMap, err := parseManifests(renderedManifests.Bytes(), logr.Discard())
	if err != nil {
		return nil, err
	}

	labelTransformer := builtins.LabelTransformerPlugin{
		Labels: originLabels(k.name, k.namespace),
		FieldSpecs: []kustypes.FieldSpec{
			{Path: "metadata/labels", CreateIfNotPresent: true},
		},
	}
	if err := labelTransformer.Transform(resMap); err != nil {
		return nil, err
	}

	yaml, err := resMap.AsYaml()
	if err != nil {
		return nil, err
	}

	return bytes.NewBuffer(yaml), nil
}

func originLabels(name, namespace string) map[string]string {
	return map[string]string{
		fmt.Sprintf("%s/name", v2.GroupVersion.Group):      name,
		fmt.Sprintf("%s/namespace", v2.GroupVersion.Group): namespace,
	}
}

// postRendererCommonMetadata applies labels and annotations from CommonMetadata.
type postRendererCommonMetadata struct {
	labels      map[string]string
	annotations map[string]string
}

func newPostRendererCommonMetadata(cm *v2.CommonMetadata) *postRendererCommonMetadata {
	return &postRendererCommonMetadata{labels: cm.Labels, annotations: cm.Annotations}
}

func (p *postRendererCommonMetadata) Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error) {
	if p.labels == nil && p.annotations == nil {
		return renderedManifests, nil
	}
	resMap, err := parseManifests(renderedManifests.Bytes(), logr.Discard())
	if err != nil {
		return nil, err
	}

	if p.labels != nil {
		labelTransformer := builtins.LabelTransformerPlugin{
			Labels: p.labels,
			FieldSpecs: []kustypes.FieldSpec{
				{Path: "metadata/labels", CreateIfNotPresent: true},
			},
		}
		if err := labelTransformer.Transform(resMap); err != nil {
			return nil, err
		}
	}

	if p.annotations != nil {
		for key, value := range p.annotations {
			lt := builtins.LabelTransformerPlugin{
				Labels: map[string]string{key: value},
				FieldSpecs: []kustypes.FieldSpec{
					{Path: "metadata/annotations", CreateIfNotPresent: true},
				},
			}
			if err := lt.Transform(resMap); err != nil {
				return nil, err
			}
		}
	}

	yaml, err := resMap.AsYaml()
	if err != nil {
		return nil, err
	}

	return bytes.NewBuffer(yaml), nil
}
