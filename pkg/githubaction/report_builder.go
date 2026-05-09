package githubaction

import (
	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/policy"
)

// ReportInput contains the domain data needed to assemble an impact report.
type ReportInput struct {
	Result             *diff.DiffResult
	PolicyResult       *policy.Result
	FullDiff           string
	MaxInlineDiffBytes int
	DiffPreviewLines   int
}

// BuildReport assembles the shared impact report used by CLI and GitHub Action adapters.
func BuildReport(input ReportInput) *ActionReport {
	result := input.Result
	if result == nil {
		return &ActionReport{Status: StatusError}
	}

	preview, truncated := TruncateDiff(input.FullDiff, input.MaxInlineDiffBytes, input.DiffPreviewLines)
	report := &ActionReport{
		Status:            StatusFromCounts(result.TotalChanged() > 0, 0, 0),
		Changed:           result.TotalChanged() > 0,
		DiffBytes:         len(input.FullDiff),
		DiffTruncated:     truncated,
		DiffPreview:       preview,
		ResourcesAdded:    len(result.Added),
		ResourcesModified: len(result.Modified),
		ResourcesDeleted:  len(result.Deleted),
		ResourcesTotal:    result.TotalChanged(),
		ByKind:            result.ByKind(),
		KindBreakdown:     diff.KindBreakdown(result),
		ByCluster:         diff.ClusterBreakdown(result),
	}
	applyPolicyResult(report, input.PolicyResult)
	return report
}

func applyPolicyResult(report *ActionReport, result *policy.Result) {
	if result == nil {
		return
	}
	report.Classifications = result.Classifications
	report.Violations = result.Violations
	report.Labels = result.Labels
	report.PolicyFailures = result.PolicyFailures
	report.PolicyFailed = result.PolicyFailed
	if report.PolicyFailed {
		report.Status = StatusError
	}
}
