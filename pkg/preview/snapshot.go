package preview

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/render"
)

// Snapshot is a detached render inventory, keyed by cluster ("" for unclustered input).
// Incomplete snapshots must not be used to infer additions or deletions.
type Snapshot struct {
	Clusters map[string]*render.Render
	Complete bool
	Warnings []string
}

// RenderSnapshot freshly loads path and returns the exact inventory used for comparison.
// On failure the returned snapshot is incomplete and the error describes why.
func (p *Preview) RenderSnapshot(ctx context.Context, path string) (*Snapshot, error) {
	snapshot := &Snapshot{Clusters: make(map[string]*render.Render)}
	results, err := p.freshLoadRepo(ctx, path)
	if err != nil {
		snapshot.Warnings = []string{err.Error()}
		return snapshot, err
	}
	diagnostics := &ExpansionError{}
	for _, cluster := range sortedClusterNames(results) {
		r := results[cluster]
		diagnostics.Errors = append(diagnostics.Errors, r.errors...)
		diagnostics.Warnings = append(diagnostics.Warnings, r.warnings...)
		if p.helmReleaseName != "" {
			r.render.FilterByLabel("helm.toolkit.fluxcd.io/name", p.helmReleaseName)
		}
		p.applyOutputOptions(r.render)
		snapshot.Clusters[cluster] = r.render
		for i, res := range r.render.Resources() {
			if _, err := res.Map(); err != nil {
				diagnostics.Errors = append(diagnostics.Errors, fmt.Errorf("cluster %q resource %d: invalid resource map: %w", cluster, i+1, err))
			}
		}
	}
	snapshot.Warnings = expansionWarnings(diagnostics)
	if err := ctx.Err(); err != nil {
		snapshot.Warnings = append(snapshot.Warnings, err.Error())
		return snapshot, err
	}
	if len(diagnostics.Errors) > 0 {
		return snapshot, diagnostics
	}
	snapshot.Complete = true
	return snapshot, nil
}

// CompareSnapshots compares complete inventories without rendering or mutating them.
// Cluster identity is retained even when resource IDs are identical across clusters.
func CompareSnapshots(ctx context.Context, before, after *Snapshot) (*diff.DiffResult, error) {
	return compareSnapshots(ctx, before, after, io.Discard)
}

func compareSnapshots(ctx context.Context, before, after *Snapshot, out io.Writer) (*diff.DiffResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, snapshot := range []*Snapshot{before, after} {
		if snapshot == nil || !snapshot.Complete || snapshot.Clusters == nil {
			return &diff.DiffResult{}, fmt.Errorf("cannot compare incomplete snapshot")
		}
		for cluster, r := range snapshot.Clusters {
			if err := ctx.Err(); err != nil {
				return &diff.DiffResult{}, err
			}
			if r == nil || r.ResMap == nil {
				return &diff.DiffResult{}, fmt.Errorf("cluster %q has no render inventory", cluster)
			}
			for _, res := range r.Resources() {
				if _, err := res.Map(); err != nil {
					return &diff.DiffResult{}, fmt.Errorf("cluster %q: invalid resource: %w", cluster, err)
				}
			}
		}
	}
	var result *diff.DiffResult
	var err error
	if len(before.Clusters) == 1 && len(after.Clusters) == 1 && before.Clusters[""] != nil && after.Clusters[""] != nil {
		result, err = diff.DiffWithResult(before.Clusters[""], after.Clusters[""], out)
	} else {
		result, err = diff.DiffWithResultClustered(before.Clusters, after.Clusters, out)
	}
	if err != nil {
		return &diff.DiffResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return &diff.DiffResult{}, err
	}
	return result, nil
}

func (p *Preview) diffSnapshots(ctx context.Context, a, b string, out io.Writer) (*diff.DiffResult, *Snapshot, *Snapshot, error) {
	// Configured filters may carry mutable state, so load sequentially.
	before, leftErr := p.RenderSnapshot(ctx, a)
	if leftErr != nil {
		var expansionErr *ExpansionError
		if !errors.As(leftErr, &expansionErr) {
			return nil, before, nil, leftErr
		}
	}
	after, rightErr := p.RenderSnapshot(ctx, b)
	if rightErr != nil || leftErr != nil {
		diagnostics := &ExpansionError{}
		for _, err := range []error{leftErr, rightErr} {
			if err == nil {
				continue
			}
			var expansionErr *ExpansionError
			if errors.As(err, &expansionErr) {
				diagnostics.Errors = append(diagnostics.Errors, expansionErr.Errors...)
				diagnostics.Warnings = append(diagnostics.Warnings, expansionErr.Warnings...)
			} else {
				return nil, before, after, errors.Join(leftErr, rightErr)
			}
		}
		return &diff.DiffResult{}, before, after, diagnostics
	}
	result, err := compareSnapshots(ctx, before, after, out)
	if err == nil && len(before.Warnings)+len(after.Warnings) > 0 {
		diagnostics := &ExpansionError{}
		for _, warning := range append(append([]string(nil), before.Warnings...), after.Warnings...) {
			diagnostics.Warnings = append(diagnostics.Warnings, errors.New(warning))
		}
		err = diagnostics
	}
	return result, before, after, err
}
