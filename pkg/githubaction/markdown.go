package githubaction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tobiash/flux-manifest-preview/pkg/policy"
)

// RenderSummaryMarkdown generates a GitHub Step Summary markdown document.
func RenderSummaryMarkdown(req *Request, report *ActionReport) string {
	var b strings.Builder

	b.WriteString("## 🔄 Flux Manifest Preview\n\n")

	statusEmoji := map[string]string{
		StatusClean:   "✅",
		StatusChanged: "📝",
		StatusWarning: "⚠️",
		StatusError:   "❌",
	}
	emoji := statusEmoji[report.Status]
	if emoji == "" {
		emoji = "❓"
	}
	_, _ = fmt.Fprintf(&b, "**Status:** %s %s\n\n", emoji, strings.ToUpper(report.Status))
	b.WriteString(renderChangeSummary(report))
	b.WriteString("\n")
	writeKindBreakdown(&b, report)
	writePolicySections(&b, report)

	if len(report.Warnings) > 0 {
		b.WriteString("### ⚠️ Warnings\n\n")
		for _, w := range report.Warnings {
			_, _ = fmt.Fprintf(&b, "- %s\n", escapeMarkdown(w))
		}
		b.WriteString("\n")
	}

	if len(report.Errors) > 0 {
		b.WriteString("### ❌ Errors\n\n")
		for _, e := range report.Errors {
			_, _ = fmt.Fprintf(&b, "- %s\n", escapeMarkdown(e))
		}
		b.WriteString("\n")
	}

	if report.DiffPreview != "" {
		b.WriteString("### Diff Preview\n\n")
		b.WriteString("<details>\n<summary>Click to expand</summary>\n\n")
		b.WriteString("```diff\n")
		b.WriteString(report.DiffPreview)
		b.WriteString("\n```\n\n")
		b.WriteString("</details>\n\n")
	}

	if report.ExportDir != "" {
		_, _ = fmt.Fprintf(&b, "### 📦 Export\n\nRendered manifests exported to `%s` (%d files).\n\n", report.ExportDir, report.ResourcesTotal)
	}

	return b.String()
}

// RenderCommentMarkdown generates a PR comment markdown document.
func RenderCommentMarkdown(req *Request, report *ActionReport) string {
	var b strings.Builder

	statusEmoji := map[string]string{
		StatusClean:   "✅",
		StatusChanged: "📝",
		StatusWarning: "⚠️",
		StatusError:   "❌",
	}
	emoji := statusEmoji[report.Status]
	if emoji == "" {
		emoji = "❓"
	}

	_, _ = fmt.Fprintf(&b, "### %s Flux Manifest Preview\n\n", emoji)

	if len(report.Errors) > 0 {
		b.WriteString("**Errors detected.**\n\n")
	} else if len(report.Warnings) > 0 {
		b.WriteString("**Completed with warnings.**\n\n")
	} else if report.Changed {
		b.WriteString("**Manifest changes detected.**\n\n")
	} else {
		b.WriteString("**No manifest changes detected.**\n\n")
	}

	b.WriteString(renderChangeSummary(report))
	b.WriteString("\n")
	writeKindBreakdown(&b, report)
	writePolicySections(&b, report)

	if len(report.Warnings) > 0 {
		b.WriteString("**Warnings:**\n")
		for _, w := range report.Warnings {
			_, _ = fmt.Fprintf(&b, "- %s\n", escapeMarkdown(w))
		}
		b.WriteString("\n")
	}

	if len(report.Errors) > 0 {
		b.WriteString("**Errors:**\n")
		for _, e := range report.Errors {
			_, _ = fmt.Fprintf(&b, "- %s\n", escapeMarkdown(e))
		}
		b.WriteString("\n")
	}

	if report.DiffPreview != "" {
		b.WriteString("<details>\n<summary>Diff Preview</summary>\n\n")
		b.WriteString("```diff\n")
		b.WriteString(report.DiffPreview)
		b.WriteString("\n```\n\n")
		b.WriteString("</details>\n\n")
	} else if report.DiffTruncated {
		b.WriteString("*Diff too large to display inline. Full diff available in workflow artifacts or outputs.*\n\n")
	}

	if report.ExportDir != "" {
		_, _ = fmt.Fprintf(&b, "📦 Exported manifests: `%s`\n\n", report.ExportDir)
	}

	b.WriteString("<!-- fmp-comment-marker -->\n")

	return b.String()
}

func escapeMarkdown(s string) string {
	// Minimal escaping for markdown inline use
	s = strings.ReplaceAll(s, "|", "\\|")
	return s
}

func renderChangeSummary(report *ActionReport) string {
	if report.ResourcesTotal == 0 {
		return "✅ **No resource changes.**\n\n"
	}

	return fmt.Sprintf(
		"🟢 **%d** to add, 🟡 **%d** to change, 🔴 **%d** to destroy.\n\n",
		report.ResourcesAdded,
		report.ResourcesModified,
		report.ResourcesDeleted,
	)
}

func writeKindBreakdown(b *strings.Builder, report *ActionReport) {
	rows := sortedKindBreakdown(report.KindBreakdown)
	if len(rows) == 0 {
		return
	}

	_, _ = fmt.Fprintf(b, "<details>\n<summary>Changed resources by kind (%d kinds)</summary>\n\n", len(rows))
	b.WriteString("| Kind | Added | Modified | Deleted | Total |\n")
	b.WriteString("| :--- | ---: | ---: | ---: | ---: |\n")
	for _, row := range rows {
		_, _ = fmt.Fprintf(
			b,
			"| %s | %d | %d | %d | %d |\n",
			row.Kind,
			row.Breakdown.Added,
			row.Breakdown.Modified,
			row.Breakdown.Deleted,
			row.Breakdown.Total,
		)
	}
	b.WriteString("\n</details>\n\n")
}

func writePolicySections(b *strings.Builder, report *ActionReport) {
	if len(report.Classifications) > 0 {
		b.WriteString("**Classifications:**\n")
		for _, item := range summarizeClassifications(report.Classifications) {
			_, _ = fmt.Fprintf(b, "- `%s` (%d)\n", item.id, item.count)
		}
		b.WriteString("\n")
	}

	if len(report.Violations) > 0 {
		b.WriteString("**Violations:**\n")
		for _, violation := range report.Violations {
			message := violation.Message
			if message == "" {
				message = violation.ID
			}
			_, _ = fmt.Fprintf(b, "- `%s`: %s\n", violation.ID, escapeMarkdown(message))
		}
		b.WriteString("\n")
	}

	if len(report.Labels) > 0 {
		b.WriteString("**Suggested labels:**\n")
		for _, label := range report.Labels {
			_, _ = fmt.Fprintf(b, "- `%s`\n", escapeMarkdown(label))
		}
		b.WriteString("\n")
	}

	if report.PolicyFailed {
		b.WriteString("**Policy enforcement failed.**\n")
		for _, id := range report.PolicyFailures {
			_, _ = fmt.Fprintf(b, "- `%s` matched `fail-on`\n", id)
		}
		b.WriteString("\n")
	}
}

type kindBreakdownRow struct {
	Kind      string
	Breakdown ChangeBreakdown
}

type summarizedClassification struct {
	id    string
	count int
}

func sortedKindBreakdown(m map[string]ChangeBreakdown) []kindBreakdownRow {
	if len(m) == 0 {
		return nil
	}

	rows := make([]kindBreakdownRow, 0, len(m))
	for kind, breakdown := range m {
		rows = append(rows, kindBreakdownRow{Kind: kind, Breakdown: breakdown})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Breakdown.Total == rows[j].Breakdown.Total {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Breakdown.Total > rows[j].Breakdown.Total
	})

	return rows
}

func summarizeClassifications(items []policy.Classification) []summarizedClassification {
	if len(items) == 0 {
		return nil
	}
	counts := make(map[string]int)
	for _, item := range items {
		counts[item.ID]++
	}
	rows := make([]summarizedClassification, 0, len(counts))
	for id, count := range counts {
		rows = append(rows, summarizedClassification{id: id, count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count == rows[j].count {
			return rows[i].id < rows[j].id
		}
		return rows[i].count > rows[j].count
	})
	return rows
}

func sortedMap(m map[string]int) [][2]string {
	var out [][2]string
	for k, v := range m {
		out = append(out, [2]string{k, fmt.Sprintf("%d", v)})
	}
	// Simple sort by key for stability
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i][0] > out[j][0] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
