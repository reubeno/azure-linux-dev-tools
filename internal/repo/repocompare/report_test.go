// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repocompare_test

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const publishChannelBase = "rpm-base"

func TestBuildPackageReportsGroupsByPackageName(t *testing.T) {
	t.Parallel()

	leftX64 := packageRecord("openssl", "1", "left-x86_64", "binary", "unsigned-x64")
	leftArm := leftX64
	leftArm.Arch = archAArch64
	leftArm.RepoID = "left-aarch64"
	leftArm.RepositoryArch = archAArch64
	leftArm.Checksum = "unsigned-arm"

	rightX64 := packageRecord("openssl", "2", "right-base-x86_64", "base", "signed")
	rightX64.Arch = archNoarch
	rightArm := rightX64
	rightArm.RepoID = "right-base-aarch64"
	rightArm.RepositoryArch = archAArch64

	reports, err := repocompare.BuildPackageReports(
		[]repocompare.Package{leftX64, leftArm},
		[]repocompare.Package{rightX64, rightArm},
		repocompare.Options{
			SkipChecksumComparison: true,
			LeftUnrouted:           true,
			CheckPublishRouting:    true,
			RightChannels: map[string][]string{
				"base": {publishChannelBase},
			},
			ResolveChannel: func(repocompare.Package) (string, error) {
				return publishChannelBase, nil
			},
		},
	)
	require.NoError(t, err)
	require.Len(t, reports, 1)

	report := reports[0]
	assert.Equal(t, "openssl", report.Name)
	assert.Equal(t, []repocompare.PackageStatus{
		repocompare.PackageStatusMissingFromRight,
		repocompare.PackageStatusAddedInRight,
	}, report.Summary)

	require.Len(t, report.Left, 1)
	assert.Equal(t, "openssl-1-1.azl4", report.Left[0].NEVR)
	require.Len(t, report.Left[0].Artifacts, 2)
	assert.Equal(t, "aarch64", report.Left[0].Artifacts[0].Arch)
	assert.Equal(t, publishChannelBase, report.Left[0].Artifacts[0].ExpectedChannel)
	assert.Equal(t, "unrouted", report.Left[0].Artifacts[0].Locations[0].Channel)
	assert.Empty(t, report.Left[0].Artifacts[0].Locations[0].Checksum)

	require.Len(t, report.Right, 1)
	require.Len(t, report.Right[0].Artifacts, 1)
	assert.Equal(t, archNoarch, report.Right[0].Artifacts[0].Arch)
	assert.Equal(t, publishChannelBase, report.Right[0].Artifacts[0].ExpectedChannel)
	assert.Equal(
		t,
		[]string{archAArch64, "x86_64"},
		report.Right[0].Artifacts[0].Locations[0].RepositoryArches,
	)
}

func TestBuildPackageReportsSummarizesArchitectureDifference(t *testing.T) {
	t.Parallel()

	left := packageRecord("pkg", "1", "left-x86_64", "base", "same")
	rightX64 := packageRecord("pkg", "1", "right-x86_64", "base", "same")
	rightArm := rightX64
	rightArm.Arch = archAArch64
	rightArm.RepoID = "right-aarch64"
	rightArm.RepositoryArch = archAArch64

	reports, err := repocompare.BuildPackageReports(
		[]repocompare.Package{left},
		[]repocompare.Package{rightX64, rightArm},
		repocompare.Options{},
	)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, []repocompare.PackageStatus{
		repocompare.PackageStatusAddedInRight,
		repocompare.PackageStatusArchitecturesDiffer,
	}, reports[0].Summary)
}

func TestBuildPackageReportsCanIgnoreOlderAdditionsInRight(t *testing.T) {
	t.Parallel()

	leftCurrent := packageRecord("pkg", "4", "left", "base", "current")
	rightCurrent := packageRecord("pkg", "4", "right-current", "base", "current")
	rightOld := packageRecord("pkg", "2", "right-old", "base", "old")

	reports, err := repocompare.BuildPackageReports(
		[]repocompare.Package{leftCurrent},
		[]repocompare.Package{rightCurrent, rightOld},
		repocompare.Options{IgnoreOlderAddedInRight: true},
	)
	require.NoError(t, err)
	assert.Empty(t, reports)
}

func TestBuildPackageReportsRetainsMissingStatusWhenIgnoringOlderRightEVR(t *testing.T) {
	t.Parallel()

	leftCurrent := packageRecord("pkg", "4", "left", "base", "current")
	rightOld := packageRecord("pkg", "2", "right-old", "base", "old")

	reports, err := repocompare.BuildPackageReports(
		[]repocompare.Package{leftCurrent},
		[]repocompare.Package{rightOld},
		repocompare.Options{IgnoreOlderAddedInRight: true},
	)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, []repocompare.PackageStatus{
		repocompare.PackageStatusMissingFromRight,
	}, reports[0].Summary)
	require.Len(t, reports[0].Right, 1)
	assert.Equal(t, "pkg-2-1.azl4", reports[0].Right[0].NEVR)
}

func TestBuildPackageReportsDoesNotIgnoreOlderEVRForDifferentArchitecture(t *testing.T) {
	t.Parallel()

	leftCurrent := packageRecord("pkg", "4", "left", "base", "current")
	rightOld := packageRecord("pkg", "2", "right-old", "base", "old")
	rightOld.Arch = archAArch64
	rightOld.RepositoryArch = archAArch64

	reports, err := repocompare.BuildPackageReports(
		[]repocompare.Package{leftCurrent},
		[]repocompare.Package{rightOld},
		repocompare.Options{IgnoreOlderAddedInRight: true},
	)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, []repocompare.PackageStatus{
		repocompare.PackageStatusMissingFromRight,
		repocompare.PackageStatusAddedInRight,
	}, reports[0].Summary)
}

func TestBuildPackageReportsUsesEmptyArrayForAbsentSide(t *testing.T) {
	t.Parallel()

	right := packageRecord("right-only", "1", "right", "base", "same")

	reports, err := repocompare.BuildPackageReports(
		nil,
		[]repocompare.Package{right},
		repocompare.Options{},
	)
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Empty(t, reports[0].Left)
	assert.Equal(t, []repocompare.PackageStatus{
		repocompare.PackageStatusAddedInRight,
	}, reports[0].Summary)

	data, err := json.Marshal(reports[0])
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name": "right-only",
		"summary": ["added-in-right"],
		"left": [],
		"right": [{
			"nevr": "right-only-1-1.azl4",
			"kind": "binary",
			"artifacts": [{
				"arch": "x86_64",
				"locations": [{
					"channel": "base",
					"repositoryArches": ["x86_64"],
					"checksum": "sha256:same",
					"size": 10
				}]
			}]
		}]
	}`, string(data))
}

func TestSummarizeReportsCountsEachPackageStatus(t *testing.T) {
	t.Parallel()

	reports := []repocompare.PackageReport{
		{
			Name: "a",
			Summary: []repocompare.PackageStatus{
				repocompare.PackageStatusMissingFromRight,
				repocompare.PackageStatusRoutingLeft,
			},
		},
		{
			Name: "b",
			Summary: []repocompare.PackageStatus{
				repocompare.PackageStatusMissingFromRight,
			},
		},
	}

	summary := repocompare.SummarizeReports(reports)
	assert.Equal(t, 2, summary.PackagesWithDifferences)
	assert.Equal(t, 2, summary.ByStatus[repocompare.PackageStatusMissingFromRight])
	assert.Equal(t, 1, summary.ByStatus[repocompare.PackageStatusRoutingLeft])
}
