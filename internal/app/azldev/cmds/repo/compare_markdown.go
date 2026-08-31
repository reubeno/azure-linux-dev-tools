// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repo

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
)

func renderComparisonMarkdown(writer io.Writer, report repocompare.Report) error {
	buffered := bufio.NewWriter(writer)

	fmt.Fprintln(buffered, "# RPM Repository Comparison")
	fmt.Fprintln(buffered)
	fmt.Fprintln(buffered, "| Left | Right | Architectures | Latest only | Checksum comparison |")
	fmt.Fprintln(buffered, "|---|---|---|---:|---|")
	fmt.Fprintf(
		buffered,
		"| %s | %s | %s | %t | %s |\n\n",
		markdownCell(report.Comparison.Left),
		markdownCell(report.Comparison.Right),
		markdownCell(strings.Join(report.Comparison.Architectures, ", ")),
		report.Comparison.LatestOnly,
		markdownCell(report.Comparison.ChecksumComparison),
	)

	fmt.Fprintln(buffered, "## Summary")
	fmt.Fprintln(buffered)
	fmt.Fprintf(
		buffered,
		"**Packages with differences:** %d\n\n",
		report.Summary.PackagesWithDifferences,
	)
	fmt.Fprintln(buffered, "| Status | Packages |")
	fmt.Fprintln(buffered, "|---|---:|")

	statuses := make([]repocompare.PackageStatus, 0, len(report.Summary.ByStatus))
	for status := range report.Summary.ByStatus {
		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i] < statuses[j]
	})

	for _, status := range statuses {
		fmt.Fprintf(
			buffered,
			"| %s | %d |\n",
			markdownCell(string(status)),
			report.Summary.ByStatus[status],
		)
	}

	renderSnapshotMarkdown(buffered, "Left snapshots", report.Snapshots.Left)
	renderSnapshotMarkdown(buffered, "Right snapshots", report.Snapshots.Right)

	fmt.Fprintln(buffered)
	fmt.Fprintln(buffered, "## Packages")

	for _, pkg := range report.Packages {
		fmt.Fprintln(buffered)
		fmt.Fprintf(buffered, "### `%s`\n\n", pkg.Name)
		fmt.Fprintf(buffered, "**Summary:** %s\n", markdownCell(joinPackageStatuses(pkg.Summary)))
		renderInventoryMarkdown(buffered, "Left", pkg.Left)
		renderInventoryMarkdown(buffered, "Right", pkg.Right)
	}

	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("writing Markdown comparison report:\n%w", err)
	}

	return nil
}

func renderSnapshotMarkdown(
	writer io.Writer,
	title string,
	snapshots []repocompare.Snapshot,
) {
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "## %s\n\n", title)
	fmt.Fprintln(writer, "| Repository | URL | Revision | repomd SHA-256 | Primary metadata | Packages |")
	fmt.Fprintln(writer, "|---|---|---|---|---|---:|")

	for _, snapshot := range snapshots {
		fmt.Fprintf(
			writer,
			"| %s | %s | %s | %s | %s | %d |\n",
			markdownCell(snapshot.RepoID),
			markdownCell(snapshot.URL),
			markdownCell(snapshot.Revision),
			markdownCell(snapshot.RepomdSHA256),
			markdownCell(snapshot.PrimaryHref),
			snapshot.PackageCount,
		)
	}
}

func renderInventoryMarkdown(writer io.Writer, title string, inventory []repocompare.NEVRReport) {
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "#### %s\n\n", title)

	if len(inventory) == 0 {
		fmt.Fprintln(writer, "_None._")

		return
	}

	fmt.Fprintln(
		writer,
		"| NEVR | Kind | Arch | Expected channel | Channel | Repository arches | Checksum | Size |",
	)
	fmt.Fprintln(writer, "|---|---|---|---|---|---|---|---:|")

	for _, nevr := range inventory {
		for _, artifact := range nevr.Artifacts {
			for _, location := range artifact.Locations {
				size := ""
				if location.Size > 0 {
					size = strconv.FormatInt(location.Size, 10)
				}

				fmt.Fprintf(
					writer,
					"| %s | %s | %s | %s | %s | %s | %s | %s |\n",
					markdownCell(nevr.NEVR),
					markdownCell(nevr.Kind),
					markdownCell(artifact.Arch),
					markdownCell(artifact.ExpectedChannel),
					markdownCell(location.Channel),
					markdownCell(strings.Join(location.RepositoryArches, ", ")),
					markdownCell(location.Checksum),
					size,
				)
			}
		}
	}
}

func joinPackageStatuses(statuses []repocompare.PackageStatus) string {
	result := make([]string, 0, len(statuses))
	for _, status := range statuses {
		result = append(result, string(status))
	}

	return strings.Join(result, ", ")
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", "<br>")

	return value
}
