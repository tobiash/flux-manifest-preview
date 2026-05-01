package githubaction

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	fmpdiff "github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/policy"
	"sigs.k8s.io/yaml"
)

type HTMLReportData struct {
	Meta      HTMLReportMeta       `json:"meta"`
	Summary   HTMLReportSummary    `json:"summary"`
	Policies  HTMLReportPolicies   `json:"policies"`
	Resources []HTMLResourceChange `json:"resources"`
}

type HTMLReportMeta struct {
	Status        string `json:"status"`
	GeneratedAt   string `json:"generatedAt"`
	Base          string `json:"base"`
	Target        string `json:"target"`
	DiffBytes     int    `json:"diffBytes"`
	DiffTruncated bool   `json:"diffTruncated"`
}

type HTMLReportSummary struct {
	Added            int                        `json:"added"`
	Modified         int                        `json:"modified"`
	Deleted          int                        `json:"deleted"`
	Total            int                        `json:"total"`
	KindBreakdown    map[string]ChangeBreakdown `json:"kindBreakdown,omitempty"`
	ClusterBreakdown map[string]ChangeBreakdown `json:"clusterBreakdown,omitempty"`
}

type HTMLReportPolicies struct {
	Classifications []policy.Classification `json:"classifications,omitempty"`
	Violations      []policy.Violation      `json:"violations,omitempty"`
	Labels          []string                `json:"labels,omitempty"`
	PolicyFailures  []string                `json:"policyFailures,omitempty"`
	PolicyFailed    bool                    `json:"policyFailed"`
}

type HTMLResourceChange struct {
	Index        int           `json:"index"`
	ID           string        `json:"id"`
	Action       string        `json:"action"`
	Cluster      string        `json:"cluster"`
	APIVersion   string        `json:"apiVersion"`
	Kind         string        `json:"kind"`
	Namespace    string        `json:"namespace"`
	Name         string        `json:"name"`
	Producer     string        `json:"producer"`
	AddedLines   int           `json:"addedLines"`
	DeletedLines int           `json:"deletedLines"`
	DiffRows     []HTMLDiffRow `json:"diffRows"`
	Truncated    bool          `json:"truncated,omitempty"`
}

type HTMLDiffRow struct {
	Type    string `json:"type"`
	OldLine int    `json:"oldLine,omitempty"`
	NewLine int    `json:"newLine,omitempty"`
	OldText string `json:"oldText,omitempty"`
	NewText string `json:"newText,omitempty"`
}

func BuildHTMLReportData(req *Request, report *ActionReport, result *fmpdiff.DiffResult) HTMLReportData {
	data := HTMLReportData{
		Meta: HTMLReportMeta{
			Status:        report.Status,
			GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
			Base:          req.DiffLeft(),
			Target:        req.DiffRight(),
			DiffBytes:     report.DiffBytes,
			DiffTruncated: report.DiffTruncated,
		},
		Summary: HTMLReportSummary{
			Added:            report.ResourcesAdded,
			Modified:         report.ResourcesModified,
			Deleted:          report.ResourcesDeleted,
			Total:            report.ResourcesTotal,
			KindBreakdown:    report.KindBreakdown,
			ClusterBreakdown: report.ByCluster,
		},
		Policies: HTMLReportPolicies{
			Classifications: report.Classifications,
			Violations:      report.Violations,
			Labels:          report.Labels,
			PolicyFailures:  report.PolicyFailures,
			PolicyFailed:    report.PolicyFailed,
		},
	}

	if result == nil {
		return data
	}

	resources := make([]HTMLResourceChange, 0, result.TotalChanged())
	resources = append(resources, htmlResourceChanges(result.Added, req.HTMLReportMaxResourceDiffBytes)...)
	resources = append(resources, htmlResourceChanges(result.Modified, req.HTMLReportMaxResourceDiffBytes)...)
	resources = append(resources, htmlResourceChanges(result.Deleted, req.HTMLReportMaxResourceDiffBytes)...)
	sort.Slice(resources, func(i, j int) bool {
		return resourceSortKey(resources[i]) < resourceSortKey(resources[j])
	})
	for i := range resources {
		resources[i].Index = i
	}
	data.Resources = resources
	return data
}

func htmlResourceChanges(changes []fmpdiff.ResourceChange, maxDiffBytes int) []HTMLResourceChange {
	out := make([]HTMLResourceChange, 0, len(changes))
	for _, change := range changes {
		before := yamlMap(change.Old)
		after := yamlMap(change.New)
		name := change.ID.String()
		unified := fmpdiff.UnifiedDiff(name, before, after)
		truncated := false
		if maxDiffBytes > 0 && len(unified) > maxDiffBytes {
			unified = unified[:maxDiffBytes]
			truncated = true
		}
		rows := parseUnifiedDiffRows(unified)
		added, deleted := countChangedRows(rows)
		apiVersion := gvkAPIVersion(change.ID.Group, change.ID.Version)
		out = append(out, HTMLResourceChange{
			ID:           resourceIdentity(change.Producer, apiVersion, change.Kind, change.Namespace, change.Name),
			Action:       change.Action,
			Cluster:      change.Cluster,
			APIVersion:   apiVersion,
			Kind:         change.Kind,
			Namespace:    change.Namespace,
			Name:         change.Name,
			Producer:     change.Producer,
			AddedLines:   added,
			DeletedLines: deleted,
			DiffRows:     rows,
			Truncated:    truncated,
		})
	}
	return out
}

func resourceSortKey(r HTMLResourceChange) string {
	return strings.Join([]string{r.Producer, r.Kind, r.Namespace, r.Name, r.Action}, "\x00")
}

func resourceIdentity(producer, apiVersion, kind, namespace, name string) string {
	return strings.Join([]string{producer, apiVersion, kind, namespace, name}, "|")
}

func yamlMap(m map[string]any) string {
	if m == nil {
		return ""
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseUnifiedDiffRows(unified string) []HTMLDiffRow {
	lines := strings.Split(strings.ReplaceAll(unified, "\r\n", "\n"), "\n")
	rows := make([]HTMLDiffRow, 0, len(lines))
	oldLine, newLine := 0, 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			oldLine, newLine = parseHunkLineNumbers(line)
			rows = append(rows, HTMLDiffRow{Type: "hunk", OldText: line, NewText: line})
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			rows = append(rows, HTMLDiffRow{Type: "added", NewLine: newLine, NewText: strings.TrimPrefix(line, "+")})
			newLine++
		case strings.HasPrefix(line, "-"):
			rows = append(rows, HTMLDiffRow{Type: "deleted", OldLine: oldLine, OldText: strings.TrimPrefix(line, "-")})
			oldLine++
		default:
			text := strings.TrimPrefix(line, " ")
			rows = append(rows, HTMLDiffRow{Type: "context", OldLine: oldLine, NewLine: newLine, OldText: text, NewText: text})
			oldLine++
			newLine++
		}
	}
	return rows
}

func parseHunkLineNumbers(line string) (int, int) {
	var oldStart, newStart int
	_, _ = fmt.Sscanf(line, "@@ -%d", &oldStart)
	if idx := strings.Index(line, " +"); idx >= 0 {
		_, _ = fmt.Sscanf(line[idx+1:], "+%d", &newStart)
	}
	return oldStart, newStart
}

func countChangedRows(rows []HTMLDiffRow) (int, int) {
	var added, deleted int
	for _, row := range rows {
		switch row.Type {
		case "added":
			added++
		case "deleted":
			deleted++
		}
	}
	return added, deleted
}

func gvkAPIVersion(group, version string) string {
	if group == "" {
		return version
	}
	return group + "/" + version
}

func reportDataJSON(data HTMLReportData) (template.JS, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	s := string(b)
	s = strings.ReplaceAll(s, "</script", "<\\/script")
	s = strings.ReplaceAll(s, "\u2028", "\\u2028")
	s = strings.ReplaceAll(s, "\u2029", "\\u2029")
	return template.JS(s), nil //nolint:gosec // JSON is marshaled and script terminators are escaped above.
}
