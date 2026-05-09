package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/policy"
)

// DiffRunOptions describes one complete rendered manifest diff run.
type DiffRunOptions struct {
	LeftPath      string
	RightPath     string
	DiffWriter    io.Writer
	Policies      *config.PolicyConfig
	PolicyBaseDir string
}

// DiffRunResult is the domain result used by CLI and GitHub Action adapters.
type DiffRunResult struct {
	Result       *diff.DiffResult
	Summary      diff.ResultSummary
	DiffText     string
	Warnings     []string
	PolicyResult *policy.Result
}

// RunDiff renders both sides, computes the rendered manifest diff, and applies policy checks.
func (p *Preview) RunDiff(ctx context.Context, opts DiffRunOptions) (*DiffRunResult, error) {
	var diffText bytes.Buffer
	result, err := p.DiffResult(ctx, opts.LeftPath, opts.RightPath, &diffText)
	if err != nil {
		var expansionErr *ExpansionError
		if !errors.As(err, &expansionErr) || result == nil {
			return nil, err
		}
	}
	if opts.DiffWriter != nil {
		if _, err := io.Copy(opts.DiffWriter, bytes.NewReader(diffText.Bytes())); err != nil {
			return nil, fmt.Errorf("writing diff text: %w", err)
		}
	}

	warnings := expansionWarnings(err)
	var policyResult *policy.Result
	if opts.Policies != nil {
		policyResult, err = policy.Evaluate(ctx, result, opts.Policies, opts.PolicyBaseDir)
		if err != nil {
			return nil, fmt.Errorf("evaluating policies: %w", err)
		}
	}

	return &DiffRunResult{
		Result:       result,
		Summary:      result.Summary(),
		DiffText:     diffText.String(),
		Warnings:     warnings,
		PolicyResult: policyResult,
	}, nil
}

func expansionWarnings(err error) []string {
	var expansionErr *ExpansionError
	if !errors.As(err, &expansionErr) {
		return nil
	}
	warnings := make([]string, 0, len(expansionErr.Errors)+len(expansionErr.Warnings))
	seen := make(map[string]struct{})
	for _, issue := range append(expansionErr.Errors, expansionErr.Warnings...) {
		if issue == nil {
			continue
		}
		msg := issue.Error()
		if _, ok := seen[msg]; ok {
			continue
		}
		seen[msg] = struct{}{}
		warnings = append(warnings, msg)
	}
	return warnings
}
