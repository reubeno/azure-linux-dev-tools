// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repocompare

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/rpm"
)

// Status identifies one comparison finding category.
type Status string

const (
	StatusLeftOnly           Status = "left-only"
	StatusRightOnly          Status = "right-only"
	StatusContentDiff        Status = "content-different"
	StatusDuplicateLeft      Status = "duplicate-left"
	StatusDuplicateRight     Status = "duplicate-right"
	StatusNoarchContentLeft  Status = "noarch-content-left"
	StatusNoarchContentRight Status = "noarch-content-right"
	StatusNoarchMissingLeft  Status = "noarch-missing-left"
	StatusNoarchMissingRight Status = "noarch-missing-right"
	StatusRoutingLeft        Status = "routing-left"
	StatusRoutingRight       Status = "routing-right"
	StatusChecksumSkipped    Status = "checksum-skipped"
	StatusSnapshotLeft       Status = "snapshot-left"
	StatusSnapshotRight      Status = "snapshot-right"
)

// Finding is one report row.
type Finding struct {
	Status            Status `json:"status"                      table:"Status"`
	Package           string `json:"package"                     table:"Package"`
	Kind              string `json:"kind"                        table:"Kind"`
	LeftRepositories  string `json:"leftRepositories,omitempty"  table:"Left repo(s)"`
	RightRepositories string `json:"rightRepositories,omitempty" table:"Right repo(s)"`
	LeftChecksum      string `json:"leftChecksum,omitempty"      table:"Left checksum"`
	RightChecksum     string `json:"rightChecksum,omitempty"     table:"Right checksum"`
	ExpectedChannel   string `json:"expectedChannel,omitempty"   table:"Expected channel"`
	Detail            string `json:"detail,omitempty"            table:"Detail"`
	name              string
}

// ChannelResolver returns the expected publish channel for a package.
type ChannelResolver func(pkg Package) (string, error)

// Options controls inventory filtering and routing checks.
type Options struct {
	LatestOnly              bool
	SkipChecksumComparison  bool
	IgnoreOlderAddedInRight bool
	CheckPublishRouting     bool
	LeftUnrouted            bool
	RightUnrouted           bool
	LeftChannels            map[string][]string
	RightChannels           map[string][]string
	ResolveChannel          ChannelResolver
}

// Compare returns every inventory, duplicate, content, and routing difference.
func Compare(left, right []Package, options Options) ([]Finding, error) {
	left, right, err := prepareInventories(left, right, options)
	if err != nil {
		return nil, err
	}

	return comparePrepared(left, right, options)
}

func prepareInventories(left, right []Package, options Options) ([]Package, []Package, error) {
	if options.LatestOnly {
		var err error

		left, err = latestBySubrepo(left)
		if err != nil {
			return nil, nil, fmt.Errorf("selecting latest packages on left:\n%w", err)
		}

		right, err = latestBySubrepo(right)
		if err != nil {
			return nil, nil, fmt.Errorf("selecting latest packages on right:\n%w", err)
		}
	}

	return left, right, nil
}

func comparePrepared(left, right []Package, options Options) ([]Finding, error) {
	leftByID := groupByIdentity(left)
	rightByID := groupByIdentity(right)
	identities := unionKeys(leftByID, rightByID)
	findings := noarchFindingsForSides(left, right, !options.SkipChecksumComparison)

	for _, identity := range identities {
		leftPackages := leftByID[identity]
		rightPackages := rightByID[identity]

		if isDuplicatePublication(leftPackages) {
			findings = append(findings, duplicateFinding(StatusDuplicateLeft, leftPackages))
		}

		if isDuplicatePublication(rightPackages) {
			findings = append(findings, duplicateFinding(StatusDuplicateRight, rightPackages))
		}

		if finding := compareIdentity(leftPackages, rightPackages, options.SkipChecksumComparison); finding != nil {
			findings = append(findings, *finding)
		}
	}

	routing, err := routingFindings(left, right, options)
	if err != nil {
		return nil, err
	}

	findings = append(findings, routing...)
	sortFindings(findings)

	return findings, nil
}

func compareIdentity(left, right []Package, skipChecksum bool) *Finding {
	if len(left) == 0 {
		finding := inventoryFinding(StatusRightOnly, right[0], nil, right)

		return &finding
	}

	if len(right) == 0 {
		finding := inventoryFinding(StatusLeftOnly, left[0], left, nil)

		return &finding
	}

	if skipChecksum {
		return nil
	}

	leftAlgorithms := checksumAlgorithms(left)

	rightAlgorithms := checksumAlgorithms(right)
	if len(leftAlgorithms) != 1 || len(rightAlgorithms) != 1 ||
		leftAlgorithms[0] != rightAlgorithms[0] {
		return &Finding{
			Status:            StatusChecksumSkipped,
			Package:           left[0].NEVRA(),
			Kind:              string(left[0].Kind),
			LeftRepositories:  repositoryIDs(left),
			RightRepositories: repositoryIDs(right),
			Detail: fmt.Sprintf(
				"checksum algorithms differ: left=%s right=%s",
				strings.Join(leftAlgorithms, ","), strings.Join(rightAlgorithms, ","),
			),
			name: left[0].Name,
		}
	}

	leftVariants := checksumVariants(left)

	rightVariants := checksumVariants(right)
	if equalStrings(leftVariants, rightVariants) {
		return nil
	}

	return &Finding{
		Status:            StatusContentDiff,
		Package:           left[0].NEVRA(),
		Kind:              string(left[0].Kind),
		LeftRepositories:  repositoryIDs(left),
		RightRepositories: repositoryIDs(right),
		LeftChecksum:      strings.Join(leftVariants, ","),
		RightChecksum:     strings.Join(rightVariants, ","),
		name:              left[0].Name,
	}
}

func latestBySubrepo(packages []Package) ([]Package, error) {
	type latestEntry struct {
		pkg     Package
		version *rpm.Version
	}

	latest := make(map[string]latestEntry)
	for _, pkg := range packages {
		key := strings.Join([]string{pkg.RepoID, pkg.Name, pkg.Arch, string(pkg.Kind)}, "\x00")

		version, err := rpm.NewVersionFromEVR(normalizedEpoch(pkg.Epoch), pkg.Version, pkg.Release)
		if err != nil {
			return nil, fmt.Errorf("invalid EVR for %s:\n%w", pkg.NEVRA(), err)
		}

		current, ok := latest[key]
		if !ok || version.GreaterThan(current.version) {
			latest[key] = latestEntry{pkg: pkg, version: version}
		}
	}

	result := make([]Package, 0, len(latest))
	for _, entry := range latest {
		result = append(result, entry.pkg)
	}

	return result, nil
}

func routingFindings(left, right []Package, options Options) ([]Finding, error) {
	if !options.CheckPublishRouting || options.ResolveChannel == nil {
		return nil, nil
	}

	var findings []Finding

	sides := []struct {
		status   Status
		packages []Package
		unrouted bool
		channels map[string][]string
	}{
		{status: StatusRoutingLeft, packages: left, unrouted: options.LeftUnrouted, channels: options.LeftChannels},
		{status: StatusRoutingRight, packages: right, unrouted: options.RightUnrouted, channels: options.RightChannels},
	}

	for _, side := range sides {
		if side.unrouted {
			continue
		}

		seen := make(map[string]struct{})

		for _, pkg := range side.packages {
			key := pkg.Identity() + "\x00" + pkg.Subrepo
			if _, ok := seen[key]; ok {
				continue
			}

			seen[key] = struct{}{}

			expected, err := options.ResolveChannel(pkg)
			if err != nil {
				return nil, fmt.Errorf("resolving publish channel for %s:\n%w", pkg.NEVRA(), err)
			}

			if contains(side.channels[pkg.Subrepo], expected) {
				continue
			}

			findings = append(findings, Finding{
				Status:          side.status,
				Package:         pkg.NEVRA(),
				Kind:            string(pkg.Kind),
				ExpectedChannel: expected,
				Detail: fmt.Sprintf(
					"package is in subrepo %s, which accepts channels %s",
					pkg.Subrepo, strings.Join(side.channels[pkg.Subrepo], ","),
				),
				name: pkg.Name,
			})
		}
	}

	return findings, nil
}

func isDuplicatePublication(packages []Package) bool {
	if len(packages) <= 1 {
		return false
	}

	if packages[0].Arch != "noarch" {
		return true
	}

	placements := make(map[string]struct{}, len(packages))

	subrepos := make(map[string]struct{}, len(packages))
	for _, pkg := range packages {
		placement := pkg.Subrepo + "\x00" + pkg.RepositoryArch
		if _, ok := placements[placement]; ok {
			return true
		}

		placements[placement] = struct{}{}
		subrepos[pkg.Subrepo] = struct{}{}
	}

	return len(subrepos) > 1
}

func noarchFindingsForSides(left, right []Package, checkContent bool) []Finding {
	findings := noarchReplicaFindings(
		StatusNoarchContentLeft, StatusNoarchMissingLeft, left, checkContent)

	return append(findings, noarchReplicaFindings(
		StatusNoarchContentRight, StatusNoarchMissingRight, right, checkContent)...)
}

func noarchReplicaFindings(
	contentStatus Status,
	missingStatus Status,
	packages []Package,
	checkContent bool,
) []Finding {
	availableArches := make(map[string]map[string]struct{})
	replicas := make(map[string][]Package)

	for _, pkg := range packages {
		if pkg.RepositoryArch != "" {
			if availableArches[pkg.Subrepo] == nil {
				availableArches[pkg.Subrepo] = make(map[string]struct{})
			}

			availableArches[pkg.Subrepo][pkg.RepositoryArch] = struct{}{}
		}

		if pkg.Arch == "noarch" {
			key := pkg.Identity() + "\x00" + pkg.Subrepo
			replicas[key] = append(replicas[key], pkg)
		}
	}

	var findings []Finding

	for _, group := range replicas {
		if checkContent && len(checksumVariants(group)) > 1 {
			findings = append(findings, Finding{
				Status:  contentStatus,
				Package: group[0].NEVRA(),
				Kind:    string(group[0].Kind),
				Detail:  "noarch replicas have different content: " + repositoryVariants(group),
				name:    group[0].Name,
			})
		}

		seenArches := make(map[string]struct{}, len(group))
		for _, pkg := range group {
			seenArches[pkg.RepositoryArch] = struct{}{}
		}

		var missing []string

		for arch := range availableArches[group[0].Subrepo] {
			if _, ok := seenArches[arch]; !ok {
				missing = append(missing, arch)
			}
		}

		if len(missing) > 0 {
			sort.Strings(missing)
			findings = append(findings, Finding{
				Status:  missingStatus,
				Package: group[0].NEVRA(),
				Kind:    string(group[0].Kind),
				Detail:  "noarch package is missing from repository architectures: " + strings.Join(missing, ","),
				name:    group[0].Name,
			})
		}
	}

	return findings
}

func groupByIdentity(packages []Package) map[string][]Package {
	grouped := make(map[string][]Package)
	for _, pkg := range packages {
		grouped[pkg.Identity()] = append(grouped[pkg.Identity()], pkg)
	}

	for key := range grouped {
		sort.Slice(grouped[key], func(i, j int) bool {
			return grouped[key][i].RepoID < grouped[key][j].RepoID
		})
	}

	return grouped
}

func unionKeys(left, right map[string][]Package) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}

	for key := range right {
		keys[key] = struct{}{}
	}

	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}

	sort.Strings(result)

	return result
}

func duplicateFinding(status Status, packages []Package) Finding {
	return Finding{
		Status:  status,
		Package: packages[0].NEVRA(),
		Kind:    string(packages[0].Kind),
		Detail:  "published in multiple physical repositories: " + repositoryVariants(packages),
		name:    packages[0].Name,
	}
}

func inventoryFinding(status Status, pkg Package, left, right []Package) Finding {
	return Finding{
		Status:            status,
		Package:           pkg.NEVRA(),
		Kind:              string(pkg.Kind),
		LeftRepositories:  repositoryIDs(left),
		RightRepositories: repositoryIDs(right),
		name:              pkg.Name,
	}
}

func repositoryIDs(packages []Package) string {
	repos := make([]string, 0, len(packages))

	seen := make(map[string]struct{})
	for _, pkg := range packages {
		if _, ok := seen[pkg.RepoID]; ok {
			continue
		}

		seen[pkg.RepoID] = struct{}{}
		repos = append(repos, pkg.RepoID)
	}

	sort.Strings(repos)

	return strings.Join(repos, ",")
}

func checksumAlgorithms(packages []Package) []string {
	algorithms := make(map[string]struct{})
	for _, pkg := range packages {
		algorithms[pkg.ChecksumType] = struct{}{}
	}

	result := make([]string, 0, len(algorithms))
	for algorithm := range algorithms {
		result = append(result, algorithm)
	}

	sort.Strings(result)

	return result
}

func checksumVariants(packages []Package) []string {
	variants := make(map[string]struct{})
	for _, pkg := range packages {
		variants[fmt.Sprintf("%s:%s(size=%d)", pkg.ChecksumType, pkg.Checksum, pkg.Size)] = struct{}{}
	}

	result := make([]string, 0, len(variants))
	for variant := range variants {
		result = append(result, variant)
	}

	sort.Strings(result)

	return result
}

func repositoryVariants(packages []Package) string {
	result := make([]string, 0, len(packages))
	for _, pkg := range packages {
		result = append(result, fmt.Sprintf(
			"%s=%s:%s(size=%d)", pkg.RepoID, pkg.ChecksumType, pkg.Checksum, pkg.Size))
	}

	sort.Strings(result)

	return strings.Join(result, ",")
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}

	return false
}

func sortFindings(findings []Finding) {
	sort.Slice(findings, func(leftIndex, rightIndex int) bool {
		if findings[leftIndex].Status != findings[rightIndex].Status {
			return findings[leftIndex].Status < findings[rightIndex].Status
		}

		if findings[leftIndex].Package != findings[rightIndex].Package {
			return findings[leftIndex].Package < findings[rightIndex].Package
		}

		return findings[leftIndex].Detail < findings[rightIndex].Detail
	})
}
