package helm

import (
	"bytes"

	v2 "github.com/fluxcd/helm-controller/api/v2"
	"helm.sh/helm/v4/pkg/postrenderer"
)

type combinedPostRenderer struct {
	renderers []postrenderer.PostRenderer
}

func newCombinedPostRenderer() combinedPostRenderer {
	return combinedPostRenderer{renderers: make([]postrenderer.PostRenderer, 0)}
}

func (c *combinedPostRenderer) addRenderer(renderer postrenderer.PostRenderer) {
	c.renderers = append(c.renderers, renderer)
}

func (c *combinedPostRenderer) Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error) {
	var result = renderedManifests
	for _, renderer := range c.renderers {
		result, err = renderer.Run(result)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// buildPostRenderers creates the post-renderer chain for a HelmRelease.
// Order: Kustomize patches → CommonMetadata → Origin labels
func buildPostRenderers(hr *v2.HelmRelease) postrenderer.PostRenderer {
	var combined = newCombinedPostRenderer()
	for _, r := range hr.Spec.PostRenderers {
		if r.Kustomize != nil {
			combined.addRenderer(newPostRendererKustomize(r.Kustomize))
		}
	}
	if hr.Spec.CommonMetadata != nil {
		combined.addRenderer(newPostRendererCommonMetadata(hr.Spec.CommonMetadata))
	}
	combined.addRenderer(newPostRendererOriginLabels(hr.Name, hr.Namespace))
	if len(combined.renderers) == 0 {
		return nil
	}
	return &combined
}
