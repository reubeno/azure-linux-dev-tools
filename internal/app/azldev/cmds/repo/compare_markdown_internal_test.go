// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repo

import (
	"bytes"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderComparisonMarkdownUsesPackageHeadingsAndInventoryTables(t *testing.T) {
	t.Parallel()

	report := repocompare.Report{
		Comparison: repocompare.ComparisonMetadata{
			Left:               "koji",
			Right:              "preview",
			Architectures:      []string{"x86_64", "aarch64"},
			ChecksumComparison: "skipped",
		},
		Summary: repocompare.ReportSummary{
			PackagesWithDifferences: 1,
			ByStatus: map[repocompare.PackageStatus]int{
				repocompare.PackageStatusMissingFromRight: 1,
			},
		},
		Packages: []repocompare.PackageReport{{
			Name:    "openssl",
			Summary: []repocompare.PackageStatus{repocompare.PackageStatusMissingFromRight},
			Left: []repocompare.NEVRReport{{
				NEVR: "openssl-1-1.azl4",
				Kind: "binary",
				Artifacts: []repocompare.ArtifactReport{{
					Arch: "noarch",
					Locations: []repocompare.LocationReport{{
						Channel:          "unrouted",
						RepositoryArches: []string{"aarch64", "x86_64"},
					}},
				}},
			}},
			Right: []repocompare.NEVRReport{},
		}},
	}

	var output bytes.Buffer

	err := renderComparisonMarkdown(&output, report)
	require.NoError(t, err)

	assert.Contains(t, output.String(), "### `openssl`")
	assert.Contains(t, output.String(), "**Summary:** missing-from-right")
	assert.Contains(t, output.String(), "| openssl-1-1.azl4 | binary | noarch |")
	assert.Contains(t, output.String(), "#### Right\n\n_None._")
}
