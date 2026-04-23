package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/sethvargo/go-githubactions"
	"github.com/spf13/cobra"

	"github.com/tobiash/flux-manifest-preview/pkg/config"
	"github.com/tobiash/flux-manifest-preview/pkg/githubaction"
	"github.com/tobiash/flux-manifest-preview/pkg/preview"
)

func githubActionCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "github-action",
		Short:  "Run fmp in GitHub Actions mode",
		Long:   `Executes a manifest diff and produces structured outputs, summaries, and comments for GitHub Actions.`,
		RunE:   runGitHubAction,
		Hidden: true,
	}
}

func runGitHubAction(cmd *cobra.Command, args []string) error {
	act := githubactions.New()
	log := cliLogger()
	inCI := os.Getenv("GITHUB_ACTIONS") == "true"

	req, err := githubaction.ParseRequestFromEnv()
	if err != nil {
		if inCI {
			act.Errorf("%v", err)
			act.SetOutput("status", githubaction.StatusError)
			act.SetOutput("errors-count", "1")
		}
		return err
	}

	report, err := executeAction(log, req)
	if err != nil {
		if inCI {
			act.Errorf("action execution failed: %v", err)
		}
		if report == nil {
			report = &githubaction.ActionReport{
				Status: githubaction.StatusError,
				Errors: []string{err.Error()},
			}
		}
	}

	// Write report artifacts
	reportDir := os.Getenv("RUNNER_TEMP")
	if reportDir == "" {
		reportDir, _ = os.MkdirTemp("", "fmp-action-*")
	}

	reportFile := filepath.Join(reportDir, "fmp-report.json")
	report.ReportFile = reportFile

	diffFile := filepath.Join(reportDir, "fmp-diff.txt")
	if report.DiffPreview != "" {
		if err := os.WriteFile(diffFile, []byte(report.DiffPreview), 0o644); err != nil {
			if inCI {
				act.Warningf("writing diff file: %v", err)
			}
		} else {
			report.DiffFile = diffFile
		}
	}

	var summaryFile, commentFile string
	if req.WriteSummary {
		summaryFile = filepath.Join(reportDir, "fmp-summary.md")
		if err := os.WriteFile(summaryFile, []byte(githubaction.RenderSummaryMarkdown(req, report)), 0o644); err != nil {
			if inCI {
				act.Warningf("writing summary: %v", err)
			}
		} else {
			report.SummaryFile = summaryFile
			if inCI && os.Getenv("GITHUB_STEP_SUMMARY") != "" {
				act.AddStepSummary(githubaction.RenderSummaryMarkdown(req, report))
			}
		}
	}

	if req.ShouldComment(report) {
		commentFile = filepath.Join(reportDir, "fmp-comment.md")
		if err := os.WriteFile(commentFile, []byte(githubaction.RenderCommentMarkdown(req, report)), 0o644); err != nil {
			if inCI {
				act.Warningf("writing comment: %v", err)
			}
		} else {
			report.CommentFile = commentFile
		}
	}

	if err := writeJSON(reportFile, report); err != nil {
		if inCI {
			act.Warningf("writing report: %v", err)
		}
	}

	// Set outputs
	if inCI {
		act.SetOutput("status", report.Status)
		act.SetOutput("changed", fmt.Sprintf("%t", report.Changed))
		act.SetOutput("warnings-count", fmt.Sprintf("%d", len(report.Warnings)))
		act.SetOutput("errors-count", fmt.Sprintf("%d", len(report.Errors)))
		act.SetOutput("resources-added", fmt.Sprintf("%d", report.ResourcesAdded))
		act.SetOutput("resources-modified", fmt.Sprintf("%d", report.ResourcesModified))
		act.SetOutput("resources-deleted", fmt.Sprintf("%d", report.ResourcesDeleted))
		act.SetOutput("resources-total", fmt.Sprintf("%d", report.ResourcesTotal))
		act.SetOutput("diff-bytes", fmt.Sprintf("%d", report.DiffBytes))
		act.SetOutput("diff-truncated", fmt.Sprintf("%t", report.DiffTruncated))
		act.SetOutput("diff-file", report.DiffFile)
		act.SetOutput("summary-file", report.SummaryFile)
		act.SetOutput("comment-file", report.CommentFile)
		act.SetOutput("report-file", report.ReportFile)
		act.SetOutput("export-dir", report.ExportDir)
	}

	if req.ShouldFail(report) {
		return fmt.Errorf("fmp action failed: status=%s errors=%d warnings=%d", report.Status, len(report.Errors), len(report.Warnings))
	}
	return nil
}

func executeAction(log logr.Logger, req *githubaction.Request) (*githubaction.ActionReport, error) {
	var diffText bytes.Buffer

	opts, err := buildActionOpts(log, req)
	if err != nil {
		return nil, err
	}

	p, err := preview.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating preview: %w", err)
	}

	left := req.DiffLeft()
	right := req.DiffRight()

	result, err := p.DiffResult(left, right, &diffText)
	if err != nil {
		// Try to produce a partial report even on error
		report := &githubaction.ActionReport{
			Status:    githubaction.StatusError,
			Errors:    []string{err.Error()},
			DiffBytes: diffText.Len(),
		}
		if diffText.Len() > 0 {
			preview, truncated := githubaction.TruncateDiff(diffText.String(), req.MaxInlineDiffBytes, req.DiffPreviewLines)
			report.DiffPreview = preview
			report.DiffTruncated = truncated
		}
		return report, err
	}

	fullDiff := diffText.String()
	preview, truncated := githubaction.TruncateDiff(fullDiff, req.MaxInlineDiffBytes, req.DiffPreviewLines)

	report := &githubaction.ActionReport{
		Status:            githubaction.StatusFromCounts(result.TotalChanged() > 0, 0, 0),
		Changed:           result.TotalChanged() > 0,
		DiffBytes:         len(fullDiff),
		DiffTruncated:     truncated,
		DiffPreview:       preview,
		ResourcesAdded:    len(result.Added),
		ResourcesModified: len(result.Modified),
		ResourcesDeleted:  len(result.Deleted),
		ResourcesTotal:    result.TotalChanged(),
		ByKind:            result.ByKind(),
	}

	// Handle exports
	if req.ExportDir != "" {
		report.ExportDir = req.ExportDir
		// Export logic would go here; for now we record the intent.
		// TODO: implement manifest export from rendered target resources
	}

	return report, nil
}

func buildActionOpts(log logr.Logger, req *githubaction.Request) ([]preview.Opt, error) {
	var cfg *config.Config
	var err error

	if req.ConfigFile != "" {
		cfg, err = config.LoadConfigFromPath(req.ConfigFile)
	} else {
		cfg, err = config.LoadConfig(req.ConfigRoot())
	}
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	paths := req.Paths
	if len(paths) == 0 && cfg != nil && len(cfg.Paths) > 0 {
		paths = cfg.Paths
	}

	// Merge config values when action inputs are at their defaults (false)
	recursive := req.Recursive
	renderHelm := req.RenderHelm
	sort := req.Sort
	excludeCRDs := req.ExcludeCRDs
	if cfg != nil {
		if !recursive && cfg.Recursive != nil {
			recursive = *cfg.Recursive
		}
		if !renderHelm && cfg.Helm != nil {
			renderHelm = *cfg.Helm
		}
		if !sort && cfg.Sort != nil {
			sort = *cfg.Sort
		}
		if !excludeCRDs && cfg.ExcludeCRDs != nil {
			excludeCRDs = *cfg.ExcludeCRDs
		}
	}

	opts := []preview.Opt{
		preview.WithLogger(log),
		preview.WithPaths(paths, recursive),
	}

	if req.ResolveGit {
		opts = append(opts, preview.WithGitRepo())
	}

	opts = append(opts, preview.WithFluxKS())

	if renderHelm {
		opts = append(opts, preview.WithHelm(helmSettings()))
	}

	if sort {
		opts = append(opts, preview.WithSort())
	}

	if excludeCRDs {
		opts = append(opts, preview.WithExcludeCRDs())
	}

	// SOPSDecrypt is blocked at request parsing; this is a defense-in-depth check
	if req.SOPSDecrypt {
		return nil, fmt.Errorf("sops-decrypt is not supported in GitHub Action mode")
	}

	if req.HelmRelease != "" {
		opts = append(opts, preview.WithHelmReleaseFilter(req.HelmRelease))
	}

	if req.FilterFile != "" {
		f, err := os.Open(req.FilterFile)
		if err != nil {
			return nil, fmt.Errorf("opening filter file: %w", err)
		}
		defer func() { _ = f.Close() }()
		opts = append(opts, preview.WithFilterFile(f))
	} else if req.FilterYAML != "" {
		opts = append(opts, preview.WithFilterYAML(req.FilterYAML))
	} else if cfg != nil && len(cfg.Filters.Filters) > 0 {
		opts = append(opts, preview.WithFilterConfig(&cfg.Filters))
	}

	return opts, nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
