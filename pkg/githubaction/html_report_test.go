package githubaction

import (
	"strings"
	"testing"
)

func TestParseUnifiedDiffRows(t *testing.T) {
	rows := parseUnifiedDiffRows("--- a\n+++ b\n@@ -2,2 +2,3 @@\n keep\n-old\n+new\n+next")
	if len(rows) != 5 {
		t.Fatalf("len(rows) = %d, want 5", len(rows))
	}
	if rows[0].Type != "hunk" {
		t.Fatalf("rows[0].Type = %q, want hunk", rows[0].Type)
	}
	if rows[1].Type != "context" || rows[1].OldLine != 2 || rows[1].NewLine != 2 {
		t.Fatalf("context row = %+v, want old/new line 2", rows[1])
	}
	if rows[2].Type != "deleted" || rows[2].OldLine != 3 || rows[2].OldText != "old" {
		t.Fatalf("deleted row = %+v", rows[2])
	}
	if rows[3].Type != "added" || rows[3].NewLine != 3 || rows[3].NewText != "new" {
		t.Fatalf("added row = %+v", rows[3])
	}
	if rows[4].Type != "added" || rows[4].NewLine != 4 || rows[4].NewText != "next" {
		t.Fatalf("added row[4] = %+v", rows[4])
	}
}

func TestRenderHTMLReportEscapesScriptTerminators(t *testing.T) {
	data := HTMLReportData{
		Meta: HTMLReportMeta{Status: StatusChanged},
		Resources: []HTMLResourceChange{{
			Kind:     "ConfigMap",
			Name:     "bad</script><script>alert(1)</script>",
			Action:   "modified",
			DiffRows: []HTMLDiffRow{{Type: "added", NewLine: 1, NewText: "</script><script>alert(1)</script>"}},
		}},
	}

	html, err := RenderHTMLReport(data)
	if err != nil {
		t.Fatalf("RenderHTMLReport() error = %v", err)
	}
	if !strings.Contains(html, "id=\"fmp-report-data\"") {
		t.Fatal("missing report data script")
	}
	if strings.Contains(html, "</script><script>alert") {
		t.Fatalf("script terminator was not escaped: %s", html)
	}
	if !strings.Contains(html, "Flux Manifest Preview Report") {
		t.Fatal("missing report title")
	}
}
