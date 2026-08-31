// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repocompare_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	archAArch64 = "aarch64"
	archNoarch  = "noarch"
)

func packageRecord(name, version, repo, subrepo, checksum string) repocompare.Package {
	return repocompare.Package{
		Name:           name,
		Epoch:          "0",
		Version:        version,
		Release:        "1.azl4",
		Arch:           "x86_64",
		Kind:           projectconfig.SubrepoKindBinary,
		ChecksumType:   "sha256",
		Checksum:       checksum,
		Size:           10,
		RepoID:         repo,
		Subrepo:        subrepo,
		RepositoryArch: "x86_64",
	}
}

func TestCompareTreatsNoarchReplicationAcrossArchitecturesAsExpected(t *testing.T) {
	t.Parallel()

	leftX64 := packageRecord("docs", "1", "left-base-x86_64", "base", "same")
	leftX64.Arch = archNoarch
	leftArm := leftX64
	leftArm.RepoID = "left-base-aarch64"
	leftArm.RepositoryArch = archAArch64

	rightX64 := packageRecord("docs", "1", "right-base-x86_64", "base", "same")
	rightX64.Arch = archNoarch
	rightArm := rightX64
	rightArm.RepoID = "right-base-aarch64"
	rightArm.RepositoryArch = archAArch64

	findings, err := repocompare.Compare(
		[]repocompare.Package{leftX64, leftArm},
		[]repocompare.Package{rightX64, rightArm},
		repocompare.Options{},
	)
	require.NoError(t, err)
	assert.Empty(t, findings)
}

func TestCompareReportsNoarchAcrossPublishChannelsAsDuplicate(t *testing.T) {
	t.Parallel()

	base := packageRecord("docs", "1", "right-base-x86_64", "base", "same")
	base.Arch = archNoarch
	sdk := base
	sdk.RepoID = "right-sdk-x86_64"
	sdk.Subrepo = "sdk"

	findings, err := repocompare.Compare(
		[]repocompare.Package{base},
		[]repocompare.Package{base, sdk},
		repocompare.Options{},
	)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, repocompare.StatusDuplicateRight, findings[0].Status)
}

func TestCompareReportsNoarchMissingArchitecture(t *testing.T) {
	t.Parallel()

	x64 := packageRecord("docs", "1", "left-base-x86_64", "base", "x64")
	x64.Arch = archNoarch
	armMarker := packageRecord("arch-package", "1", "left-base-aarch64", "base", "arm")
	armMarker.RepositoryArch = archAArch64

	findings, err := repocompare.Compare(
		[]repocompare.Package{x64, armMarker},
		[]repocompare.Package{x64, armMarker},
		repocompare.Options{},
	)
	require.NoError(t, err)

	statuses := make(map[repocompare.Status]int)
	for _, finding := range findings {
		statuses[finding.Status]++
	}

	assert.Equal(t, 1, statuses[repocompare.StatusNoarchMissingLeft])
	assert.Equal(t, 1, statuses[repocompare.StatusNoarchMissingRight])
}

func TestCompareReportsNoarchReplicaContentDifference(t *testing.T) {
	t.Parallel()

	x64 := packageRecord("docs", "1", "left-base-x86_64", "base", "x64")
	x64.Arch = archNoarch
	arm := x64
	arm.RepoID = "left-base-aarch64"
	arm.RepositoryArch = archAArch64
	arm.Checksum = "arm"

	findings, err := repocompare.Compare(
		[]repocompare.Package{x64, arm},
		[]repocompare.Package{x64, arm},
		repocompare.Options{},
	)
	require.NoError(t, err)

	statuses := make(map[repocompare.Status]int)
	for _, finding := range findings {
		statuses[finding.Status]++
	}

	assert.Equal(t, 1, statuses[repocompare.StatusNoarchContentLeft])
	assert.Equal(t, 1, statuses[repocompare.StatusNoarchContentRight])
	assert.Zero(t, statuses[repocompare.StatusDuplicateLeft])
	assert.Zero(t, statuses[repocompare.StatusDuplicateRight])
}

func TestCompareReportsInventoryContentDuplicatesAndRouting(t *testing.T) {
	t.Parallel()

	left := []repocompare.Package{
		packageRecord("left", "1", "koji", "binary", "a"),
		packageRecord("changed", "1", "koji", "binary", "a"),
	}
	right := []repocompare.Package{
		packageRecord("right", "1", "preview-sdk", "sdk", "b"),
		packageRecord("changed", "1", "preview-base", "base", "b"),
		packageRecord("changed", "1", "preview-sdk", "sdk", "b"),
	}

	findings, err := repocompare.Compare(left, right, repocompare.Options{
		CheckPublishRouting: true,
		LeftUnrouted:        true,
		RightChannels: map[string][]string{
			"base": {"rpm-base"},
			"sdk":  {"rpm-sdk"},
		},
		ResolveChannel: func(pkg repocompare.Package) (string, error) {
			if pkg.Name == "right" {
				return "rpm-base", nil
			}

			return "rpm-base", nil
		},
	})
	require.NoError(t, err)

	statuses := make(map[repocompare.Status]int)
	for _, finding := range findings {
		statuses[finding.Status]++
	}

	assert.Equal(t, 1, statuses[repocompare.StatusLeftOnly])
	assert.Equal(t, 1, statuses[repocompare.StatusRightOnly])
	assert.Equal(t, 1, statuses[repocompare.StatusContentDiff])
	assert.Equal(t, 1, statuses[repocompare.StatusDuplicateRight])
	assert.Equal(t, 2, statuses[repocompare.StatusRoutingRight])
	assert.Zero(t, statuses[repocompare.StatusRoutingLeft])
}

func TestCompareLatestOnlyIsPerPhysicalSubrepo(t *testing.T) {
	t.Parallel()

	left := []repocompare.Package{
		packageRecord("pkg", "1", "left-base", "base", "old"),
		packageRecord("pkg", "2", "left-base", "base", "new"),
		packageRecord("pkg", "1", "left-sdk", "sdk", "sdk"),
	}
	right := []repocompare.Package{
		packageRecord("pkg", "2", "right-base", "base", "new"),
		packageRecord("pkg", "1", "right-sdk", "sdk", "sdk"),
	}

	findings, err := repocompare.Compare(left, right, repocompare.Options{LatestOnly: true})
	require.NoError(t, err)
	assert.Empty(t, findings)
}

func TestCompareSkipsMixedChecksumAlgorithms(t *testing.T) {
	t.Parallel()

	left := packageRecord("pkg", "1", "left", "base", "abc")
	right := packageRecord("pkg", "1", "right", "base", "def")
	right.ChecksumType = "sha512"

	findings, err := repocompare.Compare(
		[]repocompare.Package{left},
		[]repocompare.Package{right},
		repocompare.Options{},
	)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, repocompare.StatusChecksumSkipped, findings[0].Status)
}

func TestCompareCanSkipChecksumComparison(t *testing.T) {
	t.Parallel()

	left := packageRecord("pkg", "1", "left", "base", "unsigned")
	right := packageRecord("pkg", "1", "right", "base", "signed")
	right.Size = left.Size + 1

	findings, err := repocompare.Compare(
		[]repocompare.Package{left},
		[]repocompare.Package{right},
		repocompare.Options{SkipChecksumComparison: true},
	)
	require.NoError(t, err)
	assert.Empty(t, findings)
}
