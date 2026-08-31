// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repocompare

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/rpm"
)

// PackageStatus summarizes why a package name appears in a comparison report.
type PackageStatus string

const (
	PackageStatusMissingFromRight          PackageStatus = "missing-from-right"
	PackageStatusAddedInRight              PackageStatus = "added-in-right"
	PackageStatusArchitecturesDiffer       PackageStatus = "architectures-differ"
	PackageStatusContentDifferent          PackageStatus = "content-different"
	PackageStatusContentComparisonSkipped  PackageStatus = "content-comparison-skipped"
	PackageStatusDuplicatePublicationLeft  PackageStatus = "duplicate-publication-left"
	PackageStatusDuplicatePublicationRight PackageStatus = "duplicate-publication-right"
	PackageStatusRoutingLeft               PackageStatus = "routing-left"
	PackageStatusRoutingRight              PackageStatus = "routing-right"
	PackageStatusNoarchCoverageLeft        PackageStatus = "noarch-coverage-left"
	PackageStatusNoarchCoverageRight       PackageStatus = "noarch-coverage-right"
)

// ComparisonMetadata describes the inputs and modes used to produce a report.
type ComparisonMetadata struct {
	Left                    string   `json:"left"`
	Right                   string   `json:"right"`
	Architectures           []string `json:"architectures"`
	LatestOnly              bool     `json:"latestOnly"`
	ChecksumComparison      string   `json:"checksumComparison"`
	IgnoreOlderAddedInRight bool     `json:"ignoreOlderAddedInRight"`
}

// ReportSummary contains package-level aggregate counts.
type ReportSummary struct {
	PackagesWithDifferences int                   `json:"packagesWithDifferences"`
	ByStatus                map[PackageStatus]int `json:"byStatus"`
}

// ReportSnapshots records the repository metadata used for each side.
type ReportSnapshots struct {
	Left  []Snapshot `json:"left"`
	Right []Snapshot `json:"right"`
}

// Report is the canonical package-centric repository comparison result.
type Report struct {
	Comparison ComparisonMetadata `json:"comparison"`
	Summary    ReportSummary      `json:"summary"`
	Snapshots  ReportSnapshots    `json:"snapshots"`
	Packages   []PackageReport    `json:"packages"`
}

// PackageReport contains both inventories for one differing RPM package name.
type PackageReport struct {
	Name    string          `json:"name"`
	Summary []PackageStatus `json:"summary"`
	Left    []NEVRReport    `json:"left"`
	Right   []NEVRReport    `json:"right"`
}

// NEVRReport groups all RPM architectures for one NEVR and artifact kind.
type NEVRReport struct {
	NEVR      string           `json:"nevr"`
	Kind      string           `json:"kind"`
	Artifacts []ArtifactReport `json:"artifacts"`
	version   *rpm.Version
}

// ArtifactReport describes one RPM architecture and all of its publication locations.
type ArtifactReport struct {
	Arch            string           `json:"arch"`
	ExpectedChannel string           `json:"expectedChannel,omitempty"`
	Locations       []LocationReport `json:"locations"`
}

// LocationReport describes matching package content in one logical repository channel.
type LocationReport struct {
	Channel          string   `json:"channel"`
	RepositoryArches []string `json:"repositoryArches"`
	Checksum         string   `json:"checksum,omitempty"`
	Size             int64    `json:"size,omitempty"`
}

// SummaryRow is the compact table and CSV representation of one package report.
type SummaryRow struct {
	Name       string `json:"name"       table:"Name"`
	Summary    string `json:"summary"    table:"Summary"`
	LeftNEVRs  string `json:"leftNevrs"  table:"Left NEVRs"`
	RightNEVRs string `json:"rightNevrs" table:"Right NEVRs"`
}

type artifactGroup struct {
	pkg       Package
	locations map[string]*LocationReport
}

type nevrGroup struct {
	pkg       Package
	artifacts map[string]*artifactGroup
}

// BuildPackageReports converts detailed comparison results into package-centric inventories.
func BuildPackageReports(
	left, right []Package,
	options Options,
) ([]PackageReport, error) {
	left, right, err := prepareInventories(left, right, options)
	if err != nil {
		return nil, err
	}

	findings, err := comparePrepared(left, right, options)
	if err != nil {
		return nil, err
	}

	findingsByName := groupFindingsByName(findings)
	names := differingPackageNames(findingsByName)
	leftByName := groupByName(left)
	rightByName := groupByName(right)

	reports := make([]PackageReport, 0, len(names))
	for _, name := range names {
		leftInventory, err := buildInventory(leftByName[name], options, true)
		if err != nil {
			return nil, fmt.Errorf("building left inventory for package %#q:\n%w", name, err)
		}

		rightInventory, err := buildInventory(rightByName[name], options, false)
		if err != nil {
			return nil, fmt.Errorf("building right inventory for package %#q:\n%w", name, err)
		}

		report := PackageReport{
			Name:    name,
			Summary: packageStatuses(leftInventory, rightInventory, findingsByName[name], options),
			Left:    leftInventory,
			Right:   rightInventory,
		}
		if len(report.Summary) > 0 {
			reports = append(reports, report)
		}
	}

	return reports, nil
}

// SummarizeReports computes package-level counts for a canonical report.
func SummarizeReports(reports []PackageReport) ReportSummary {
	summary := ReportSummary{
		PackagesWithDifferences: len(reports),
		ByStatus:                make(map[PackageStatus]int),
	}

	for _, report := range reports {
		for _, status := range report.Summary {
			summary.ByStatus[status]++
		}
	}

	return summary
}

// SummaryRows returns one compact row per differing package name.
func SummaryRows(reports []PackageReport) []SummaryRow {
	rows := make([]SummaryRow, 0, len(reports))
	for _, report := range reports {
		rows = append(rows, SummaryRow{
			Name:       report.Name,
			Summary:    joinStatuses(report.Summary),
			LeftNEVRs:  joinNEVRs(report.Left),
			RightNEVRs: joinNEVRs(report.Right),
		})
	}

	return rows
}

func groupFindingsByName(findings []Finding) map[string][]Finding {
	result := make(map[string][]Finding)

	for _, finding := range findings {
		if finding.name != "" {
			result[finding.name] = append(result[finding.name], finding)
		}
	}

	return result
}

func differingPackageNames(findingsByName map[string][]Finding) []string {
	names := make([]string, 0, len(findingsByName))
	for name := range findingsByName {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func groupByName(packages []Package) map[string][]Package {
	result := make(map[string][]Package)
	for _, pkg := range packages {
		result[pkg.Name] = append(result[pkg.Name], pkg)
	}

	return result
}

func buildInventory(packages []Package, options Options, left bool) ([]NEVRReport, error) {
	groups := make(map[string]*nevrGroup)
	for _, pkg := range packages {
		addInventoryPackage(groups, pkg, options, left)
	}

	inventory := make([]NEVRReport, 0, len(groups))
	for _, group := range groups {
		entry, err := buildNEVRReport(group, options)
		if err != nil {
			return nil, err
		}

		inventory = append(inventory, entry)
	}

	sortNEVRReports(inventory)

	return inventory, nil
}

func addInventoryPackage(
	groups map[string]*nevrGroup,
	pkg Package,
	options Options,
	left bool,
) {
	nevrKey := pkg.NEVR() + "\x00" + string(pkg.Kind)

	group := groups[nevrKey]
	if group == nil {
		group = &nevrGroup{pkg: pkg, artifacts: make(map[string]*artifactGroup)}
		groups[nevrKey] = group
	}

	artifact := group.artifacts[pkg.Arch]
	if artifact == nil {
		artifact = &artifactGroup{pkg: pkg, locations: make(map[string]*LocationReport)}
		group.artifacts[pkg.Arch] = artifact
	}

	channel := pkg.Subrepo
	if sideUnrouted(options, left) {
		channel = "unrouted"
	}

	locationKey := channel
	if !options.SkipChecksumComparison {
		locationKey += "\x00" + pkg.ChecksumType + "\x00" + pkg.Checksum +
			fmt.Sprintf("\x00%d", pkg.Size)
	}

	location := artifact.locations[locationKey]
	if location == nil {
		location = newLocationReport(channel, pkg, options.SkipChecksumComparison)
		artifact.locations[locationKey] = location
	}

	if pkg.RepositoryArch != "" && !contains(location.RepositoryArches, pkg.RepositoryArch) {
		location.RepositoryArches = append(location.RepositoryArches, pkg.RepositoryArch)
	}
}

func newLocationReport(channel string, pkg Package, skipChecksum bool) *LocationReport {
	location := &LocationReport{
		Channel:          channel,
		RepositoryArches: make([]string, 0),
	}
	if !skipChecksum {
		location.Checksum = pkg.ChecksumType + ":" + pkg.Checksum
		location.Size = pkg.Size
	}

	return location
}

func buildNEVRReport(group *nevrGroup, options Options) (NEVRReport, error) {
	version, err := rpm.NewVersionFromEVR(
		normalizedEpoch(group.pkg.Epoch), group.pkg.Version, group.pkg.Release)
	if err != nil {
		return NEVRReport{}, fmt.Errorf("invalid EVR for %s:\n%w", group.pkg.NEVRA(), err)
	}

	entry := NEVRReport{
		NEVR:    group.pkg.NEVR(),
		Kind:    string(group.pkg.Kind),
		version: version,
	}

	for _, groupedArtifact := range group.artifacts {
		artifact, err := buildArtifactReport(groupedArtifact, options)
		if err != nil {
			return NEVRReport{}, err
		}

		entry.Artifacts = append(entry.Artifacts, artifact)
	}

	sort.Slice(entry.Artifacts, func(leftIndex, rightIndex int) bool {
		return entry.Artifacts[leftIndex].Arch < entry.Artifacts[rightIndex].Arch
	})

	return entry, nil
}

func buildArtifactReport(
	group *artifactGroup,
	options Options,
) (ArtifactReport, error) {
	artifact := ArtifactReport{Arch: group.pkg.Arch}

	if options.CheckPublishRouting && options.ResolveChannel != nil {
		expected, err := options.ResolveChannel(group.pkg)
		if err != nil {
			return ArtifactReport{}, fmt.Errorf(
				"resolving publish channel for %s:\n%w", group.pkg.NEVRA(), err)
		}

		artifact.ExpectedChannel = expected
	}

	for _, location := range group.locations {
		sort.Strings(location.RepositoryArches)
		artifact.Locations = append(artifact.Locations, *location)
	}

	sort.Slice(artifact.Locations, func(leftIndex, rightIndex int) bool {
		leftLocation := artifact.Locations[leftIndex]

		rightLocation := artifact.Locations[rightIndex]
		if leftLocation.Channel != rightLocation.Channel {
			return leftLocation.Channel < rightLocation.Channel
		}

		return leftLocation.Checksum < rightLocation.Checksum
	})

	return artifact, nil
}

func sideUnrouted(options Options, left bool) bool {
	return (left && options.LeftUnrouted) || (!left && options.RightUnrouted)
}

func sortNEVRReports(inventory []NEVRReport) {
	sort.Slice(inventory, func(leftIndex, rightIndex int) bool {
		comparison := inventory[leftIndex].version.Compare(inventory[rightIndex].version)
		if comparison != 0 {
			return comparison > 0
		}

		return inventory[leftIndex].Kind < inventory[rightIndex].Kind
	})
}

func packageStatuses(
	left, right []NEVRReport,
	findings []Finding,
	options Options,
) []PackageStatus {
	statuses := make(map[PackageStatus]struct{})

	leftIdentities := inventoryIdentitySet(left)
	rightIdentities := inventoryIdentitySet(right)

	if hasSetDifference(leftIdentities, rightIdentities) {
		statuses[PackageStatusMissingFromRight] = struct{}{}
	}

	if hasUnignoredRightAddition(left, right, leftIdentities, options.IgnoreOlderAddedInRight) {
		statuses[PackageStatusAddedInRight] = struct{}{}
	}

	if architecturesDiffer(left, right) {
		statuses[PackageStatusArchitecturesDiffer] = struct{}{}
	}

	for _, finding := range findings {
		if status, ok := packageStatusForFinding(finding.Status); ok {
			statuses[status] = struct{}{}
		}
	}

	result := make([]PackageStatus, 0, len(statuses))
	for status := range statuses {
		result = append(result, status)
	}

	sort.Slice(result, func(i, j int) bool {
		return packageStatusRank(result[i]) < packageStatusRank(result[j])
	})

	return result
}

func packageStatusForFinding(status Status) (PackageStatus, bool) {
	switch status {
	case StatusContentDiff, StatusNoarchContentLeft, StatusNoarchContentRight:
		return PackageStatusContentDifferent, true
	case StatusChecksumSkipped:
		return PackageStatusContentComparisonSkipped, true
	case StatusDuplicateLeft:
		return PackageStatusDuplicatePublicationLeft, true
	case StatusDuplicateRight:
		return PackageStatusDuplicatePublicationRight, true
	case StatusRoutingLeft:
		return PackageStatusRoutingLeft, true
	case StatusRoutingRight:
		return PackageStatusRoutingRight, true
	case StatusNoarchMissingLeft:
		return PackageStatusNoarchCoverageLeft, true
	case StatusNoarchMissingRight:
		return PackageStatusNoarchCoverageRight, true
	case StatusLeftOnly, StatusRightOnly, StatusSnapshotLeft, StatusSnapshotRight:
		return "", false
	default:
		return "", false
	}
}

func inventoryIdentitySet(inventory []NEVRReport) map[string]struct{} {
	identities := make(map[string]struct{})

	for _, entry := range inventory {
		for _, artifact := range entry.Artifacts {
			key := strings.Join([]string{entry.NEVR, entry.Kind, artifact.Arch}, "\x00")
			identities[key] = struct{}{}
		}
	}

	return identities
}

func hasSetDifference(left, right map[string]struct{}) bool {
	for identity := range left {
		if _, ok := right[identity]; !ok {
			return true
		}
	}

	return false
}

func hasUnignoredRightAddition(
	left, right []NEVRReport,
	leftIdentities map[string]struct{},
	ignoreOlder bool,
) bool {
	if !ignoreOlder {
		return hasSetDifference(inventoryIdentitySet(right), leftIdentities)
	}

	latestLeft := latestVersionsByKindAndArch(left)

	for _, entry := range right {
		for _, artifact := range entry.Artifacts {
			identity := inventoryIdentity(entry, artifact)
			if _, ok := leftIdentities[identity]; ok {
				continue
			}

			matchKey := entry.Kind + "\x00" + artifact.Arch

			leftVersion, ok := latestLeft[matchKey]
			if !ok || entry.version.Compare(leftVersion) >= 0 {
				return true
			}
		}
	}

	return false
}

func latestVersionsByKindAndArch(inventory []NEVRReport) map[string]*rpm.Version {
	result := make(map[string]*rpm.Version)

	for _, entry := range inventory {
		for _, artifact := range entry.Artifacts {
			key := entry.Kind + "\x00" + artifact.Arch

			current, ok := result[key]
			if !ok || entry.version.GreaterThan(current) {
				result[key] = entry.version
			}
		}
	}

	return result
}

func inventoryIdentity(entry NEVRReport, artifact ArtifactReport) string {
	return strings.Join([]string{entry.NEVR, entry.Kind, artifact.Arch}, "\x00")
}

func architecturesDiffer(left, right []NEVRReport) bool {
	leftArches := archesByNEVR(left)
	rightArches := archesByNEVR(right)

	for key, arches := range leftArches {
		if other, ok := rightArches[key]; ok && !equalStrings(arches, other) {
			return true
		}
	}

	return false
}

func archesByNEVR(inventory []NEVRReport) map[string][]string {
	result := make(map[string][]string)

	for _, entry := range inventory {
		key := entry.NEVR + "\x00" + entry.Kind
		for _, artifact := range entry.Artifacts {
			result[key] = append(result[key], artifact.Arch)
		}

		sort.Strings(result[key])
	}

	return result
}

func packageStatusRank(status PackageStatus) int {
	order := []PackageStatus{
		PackageStatusMissingFromRight,
		PackageStatusAddedInRight,
		PackageStatusArchitecturesDiffer,
		PackageStatusContentDifferent,
		PackageStatusContentComparisonSkipped,
		PackageStatusDuplicatePublicationLeft,
		PackageStatusDuplicatePublicationRight,
		PackageStatusRoutingLeft,
		PackageStatusRoutingRight,
		PackageStatusNoarchCoverageLeft,
		PackageStatusNoarchCoverageRight,
	}

	for index, candidate := range order {
		if candidate == status {
			return index
		}
	}

	return len(order)
}

func joinStatuses(statuses []PackageStatus) string {
	values := make([]string, 0, len(statuses))
	for _, status := range statuses {
		values = append(values, string(status))
	}

	return strings.Join(values, ", ")
}

func joinNEVRs(inventory []NEVRReport) string {
	nevrs := make([]string, 0, len(inventory))
	seen := make(map[string]struct{})

	for _, entry := range inventory {
		if _, ok := seen[entry.NEVR]; ok {
			continue
		}

		seen[entry.NEVR] = struct{}{}
		nevrs = append(nevrs, entry.NEVR)
	}

	return strings.Join(nevrs, ", ")
}
