package helm

import "bytes"

// PostRenderer is the minimal interface fmp needs for post-rendering Helm output.
// We intentionally keep this local instead of importing Helm's pkg/postrenderer,
// which drags in Helm's plugin runtime stack.
type PostRenderer interface {
	Run(renderedManifests *bytes.Buffer) (modifiedManifests *bytes.Buffer, err error)
}
