package preview

import (
	"context"
	"fmt"
	"io"

	"github.com/tobiash/flux-manifest-preview/pkg/diff"
)

type normalizationDiscovery struct {
	preview *Preview
	path    string
}

type NormalizationFinding struct {
	Cluster string
	Diff    diff.FieldDiff
}

type NormalizationDiagnosis struct {
	Findings []NormalizationFinding
}

func (d NormalizationDiagnosis) FieldDiffs() []diff.FieldDiff {
	diffs := make([]diff.FieldDiff, 0, len(d.Findings))
	for _, finding := range d.Findings {
		diffs = append(diffs, finding.Diff)
	}
	return diffs
}

func (d normalizationDiscovery) WritePermadiffConfig(ctx context.Context, out io.Writer) error {
	left, right, err := d.renderTwice(ctx)
	if err != nil {
		return err
	}

	clusters := sortedClusterNames(left)
	for _, cluster := range clusters {
		if err := diff.WritePermadiffConfig(left[cluster].render, right[cluster].render, out); err != nil {
			if d.preview.isClustered() {
				return fmt.Errorf("cluster %q: %w", cluster, err)
			}
			return err
		}
	}
	return nil
}

func (d normalizationDiscovery) Detect(ctx context.Context) ([]diff.FieldDiff, error) {
	diagnosis, err := d.Diagnose(ctx)
	if err != nil {
		return nil, err
	}
	return diagnosis.FieldDiffs(), nil
}

func (d normalizationDiscovery) Diagnose(ctx context.Context) (*NormalizationDiagnosis, error) {
	left, right, err := d.renderTwice(ctx)
	if err != nil {
		return nil, err
	}

	diagnosis := &NormalizationDiagnosis{}
	clusters := sortedClusterNames(left)
	for _, cluster := range clusters {
		diffs, err := diff.DetectPermadiffs(left[cluster].render, right[cluster].render)
		if err != nil {
			if d.preview.isClustered() {
				return nil, fmt.Errorf("cluster %q: detecting permadiffs: %w", cluster, err)
			}
			return nil, fmt.Errorf("detecting permadiffs: %w", err)
		}
		for _, fieldDiff := range diffs {
			diagnosis.Findings = append(diagnosis.Findings, NormalizationFinding{
				Cluster: cluster,
				Diff:    fieldDiff,
			})
		}
	}
	return diagnosis, nil
}

func (d normalizationDiscovery) renderTwice(ctx context.Context) (map[string]*loadRepoResult, map[string]*loadRepoResult, error) {
	left, err := d.preview.freshLoadRepo(ctx, d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading repo (first pass): %w", err)
	}

	right, err := d.preview.freshLoadRepo(ctx, d.path)
	if err != nil {
		return nil, nil, fmt.Errorf("error loading repo (second pass): %w", err)
	}

	return left, right, nil
}
