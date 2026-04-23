package githubaction

import (
	"strings"
	"testing"
)

func TestRenderSummaryMarkdown(t *testing.T) {
	req := &Request{WriteSummary: true}
	report := &ActionReport{
		Status:            StatusChanged,
		Changed:           true,
		ResourcesAdded:    2,
		ResourcesModified: 5,
		ResourcesDeleted:  1,
		ResourcesTotal:    8,
		ByKind:            map[string]int{"Deployment": 3, "ConfigMap": 2},
		Warnings:          []string{"helm chart not found"},
		DiffPreview:       "@@ -1 +1 @@\n-foo\n+bar",
	}

	md := RenderSummaryMarkdown(req, report)
	if !strings.Contains(md, "Flux Manifest Preview") {
		t.Error("missing title")
	}
	if !strings.Contains(md, "CHANGED") {
		t.Error("missing status")
	}
	if !strings.Contains(md, "2") {
		t.Error("missing added count")
	}
	if !strings.Contains(md, "ConfigMap") {
		t.Error("missing kind breakdown")
	}
	if !strings.Contains(md, "helm chart not found") {
		t.Error("missing warning")
	}
	if !strings.Contains(md, "```diff") {
		t.Error("missing diff code block")
	}
}

func TestRenderCommentMarkdown(t *testing.T) {
	req := &Request{Comment: true, CommentMode: "changes"}
	report := &ActionReport{
		Status:            StatusClean,
		Changed:           false,
		ResourcesAdded:    0,
		ResourcesModified: 0,
		ResourcesDeleted:  0,
		ResourcesTotal:    0,
	}

	md := RenderCommentMarkdown(req, report)
	if !strings.Contains(md, "No manifest changes detected") {
		t.Error("missing no-changes text")
	}
	if !strings.Contains(md, "fmp-comment-marker") {
		t.Error("missing comment marker")
	}
}

func TestRenderCommentMarkdownWithDiff(t *testing.T) {
	req := &Request{Comment: true}
	report := &ActionReport{
		Status:            StatusChanged,
		Changed:           true,
		ResourcesAdded:    1,
		ResourcesModified: 0,
		ResourcesDeleted:  0,
		ResourcesTotal:    1,
		DiffPreview:       "+apiVersion: v1",
		ByKind:            map[string]int{"ConfigMap": 1},
	}

	md := RenderCommentMarkdown(req, report)
	if !strings.Contains(md, "Manifest changes detected") {
		t.Error("missing changes text")
	}
	if !strings.Contains(md, "```diff") {
		t.Error("missing diff block")
	}
	if !strings.Contains(md, "ConfigMap") {
		t.Error("missing kind table")
	}
}

func TestEscapeMarkdown(t *testing.T) {
	if escapeMarkdown("a|b") != "a\\|b" {
		t.Error("pipe not escaped")
	}
}
