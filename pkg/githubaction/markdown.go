package githubaction

import (
	"fmt"
	"strings"
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

	b.WriteString("### Resource Changes\n\n")
	b.WriteString("| Metric | Count |\n")
	b.WriteString("| :--- | ---: |\n")
	_, _ = fmt.Fprintf(&b, "| Added | %d |\n", report.ResourcesAdded)
	_, _ = fmt.Fprintf(&b, "| Modified | %d |\n", report.ResourcesModified)
	_, _ = fmt.Fprintf(&b, "| Deleted | %d |\n", report.ResourcesDeleted)
	_, _ = fmt.Fprintf(&b, "| Total Changed | %d |\n\n", report.ResourcesTotal)

	if len(report.ByKind) > 0 {
		b.WriteString("### By Kind\n\n")
		b.WriteString("| Kind | Count |\n")
		b.WriteString("| :--- | ---: |\n")
		for _, pair := range sortedMap(report.ByKind) {
			_, _ = fmt.Fprintf(&b, "| %s | %s |\n", pair[0], pair[1])
		}
		b.WriteString("\n")
	}

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

	b.WriteString("| Metric | Count |\n")
	b.WriteString("| :--- | ---: |\n")
	_, _ = fmt.Fprintf(&b, "| Added | %d |\n", report.ResourcesAdded)
	_, _ = fmt.Fprintf(&b, "| Modified | %d |\n", report.ResourcesModified)
	_, _ = fmt.Fprintf(&b, "| Deleted | %d |\n", report.ResourcesDeleted)
	_, _ = fmt.Fprintf(&b, "| Total Changed | %d |\n\n", report.ResourcesTotal)

	if len(report.ByKind) > 0 {
		b.WriteString("| Kind | Count |\n")
		b.WriteString("| :--- | ---: |\n")
		for _, pair := range sortedMap(report.ByKind) {
			_, _ = fmt.Fprintf(&b, "| %s | %s |\n", pair[0], pair[1])
		}
		b.WriteString("\n")
	}

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
