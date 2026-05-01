package main

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/tobiash/flux-manifest-preview/pkg/diff"
	"github.com/tobiash/flux-manifest-preview/pkg/githubaction"
	"github.com/tobiash/flux-manifest-preview/pkg/policy"
)

func writeDiffSummary(out io.Writer, result *diff.DiffResult, policyResult *policy.Result) error {
	if result == nil {
		return nil
	}

	if _, err := fmt.Fprintln(out, classifyDiff(result)); err != nil {
		return err
	}

	if result.TotalChanged() == 0 {
		_, err := fmt.Fprintln(out, "✅ No resource changes.")
		return err
	}

	if _, err := fmt.Fprintf(
		out,
		"🟢 %d to add, 🟡 %d to change, 🔴 %d to destroy.\n\n",
		len(result.Added),
		len(result.Modified),
		len(result.Deleted),
	); err != nil {
		return err
	}

	rows := sortedKindBreakdownRows(buildKindBreakdown(result))
	if len(rows) == 0 {
		return nil
	}

	if _, err := fmt.Fprintln(out, "Changed resources by kind:"); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "KIND\tADDED\tMODIFIED\tDELETED\tTOTAL"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(
			tw,
			"%s\t%d\t%d\t%d\t%d\n",
			row.Kind,
			row.Added,
			row.Modified,
			row.Deleted,
			row.Total,
		); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	return writePolicySummary(out, policyResult)
}

func classifyDiff(result *diff.DiffResult) string {
	total := result.TotalChanged()
	if total == 0 {
		return "No manifest changes detected."
	}

	added := len(result.Added)
	modified := len(result.Modified)
	deleted := len(result.Deleted)

	switch dominantChangeKind(added, modified, deleted, total) {
	case "added":
		return "Mostly additive changes detected."
	case "modified":
		return "Mostly in-place changes detected."
	case "deleted":
		return "Mostly destructive changes detected."
	default:
		return "Mixed manifest changes detected."
	}
}

func dominantChangeKind(added, modified, deleted, total int) string {
	threshold := total * 7
	if added*10 >= threshold {
		return "added"
	}
	if modified*10 >= threshold {
		return "modified"
	}
	if deleted*10 >= threshold {
		return "deleted"
	}
	return ""
}

type kindBreakdownRow struct {
	Kind     string
	Added    int
	Modified int
	Deleted  int
	Total    int
}

func buildKindBreakdown(result *diff.DiffResult) map[string]githubaction.ChangeBreakdown {
	breakdown := make(map[string]githubaction.ChangeBreakdown)

	for _, change := range result.Added {
		entry := breakdown[change.Kind]
		entry.Added++
		entry.Total++
		breakdown[change.Kind] = entry
	}

	for _, change := range result.Modified {
		entry := breakdown[change.Kind]
		entry.Modified++
		entry.Total++
		breakdown[change.Kind] = entry
	}

	for _, change := range result.Deleted {
		entry := breakdown[change.Kind]
		entry.Deleted++
		entry.Total++
		breakdown[change.Kind] = entry
	}

	return breakdown
}

func buildClusterBreakdown(result *diff.DiffResult) map[string]githubaction.ChangeBreakdown {
	breakdown := make(map[string]githubaction.ChangeBreakdown)

	for _, change := range result.Added {
		entry := breakdown[change.Cluster]
		entry.Added++
		entry.Total++
		breakdown[change.Cluster] = entry
	}

	for _, change := range result.Modified {
		entry := breakdown[change.Cluster]
		entry.Modified++
		entry.Total++
		breakdown[change.Cluster] = entry
	}

	for _, change := range result.Deleted {
		entry := breakdown[change.Cluster]
		entry.Deleted++
		entry.Total++
		breakdown[change.Cluster] = entry
	}

	return breakdown
}

func sortedKindBreakdownRows(m map[string]githubaction.ChangeBreakdown) []kindBreakdownRow {
	if len(m) == 0 {
		return nil
	}

	rows := make([]kindBreakdownRow, 0, len(m))
	for kind, row := range m {
		rows = append(rows, kindBreakdownRow{
			Kind:     kind,
			Added:    row.Added,
			Modified: row.Modified,
			Deleted:  row.Deleted,
			Total:    row.Total,
		})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Total == rows[j].Total {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Total > rows[j].Total
	})

	return rows
}

func writePolicySummary(out io.Writer, result *policy.Result) error {
	if result == nil {
		return nil
	}

	if len(result.Classifications) > 0 {
		if _, err := fmt.Fprintln(out, "Classifications:"); err != nil {
			return err
		}
		for _, item := range summarizePolicyClassifications(result.Classifications) {
			if _, err := fmt.Fprintf(out, "- %s (%d)\n", item.id, item.count); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}

	if len(result.Violations) > 0 {
		if _, err := fmt.Fprintln(out, "Violations:"); err != nil {
			return err
		}
		for _, violation := range result.Violations {
			message := violation.Message
			if message == "" {
				message = violation.ID
			}
			if _, err := fmt.Fprintf(out, "- %s: %s\n", violation.ID, message); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}

	if len(result.Labels) > 0 {
		if _, err := fmt.Fprintln(out, "Suggested labels:"); err != nil {
			return err
		}
		for _, label := range result.Labels {
			if _, err := fmt.Fprintf(out, "- %s\n", label); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}

	if result.PolicyFailed {
		if _, err := fmt.Fprintln(out, "Policy enforcement failed:"); err != nil {
			return err
		}
		for _, id := range result.PolicyFailures {
			if _, err := fmt.Fprintf(out, "- %s\n", id); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}

	return nil
}

type summarizedPolicyClassification struct {
	id    string
	count int
}

func summarizePolicyClassifications(items []policy.Classification) []summarizedPolicyClassification {
	counts := make(map[string]int)
	for _, item := range items {
		counts[item.ID]++
	}
	rows := make([]summarizedPolicyClassification, 0, len(counts))
	for id, count := range counts {
		rows = append(rows, summarizedPolicyClassification{id: id, count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].count == rows[j].count {
			return rows[i].id < rows[j].id
		}
		return rows[i].count > rows[j].count
	})
	return rows
}
