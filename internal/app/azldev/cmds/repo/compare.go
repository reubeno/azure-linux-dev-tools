// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package repo

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repocompare"
	"github.com/microsoft/azure-linux-dev-tools/internal/repo/repolayout"
	"github.com/spf13/cobra"
)

// CompareOptions are the CLI flags for `azldev repo compare`.
type CompareOptions struct {
	Comparison              string
	Left                    string
	Right                   string
	Arches                  []string
	LatestOnly              bool
	LatestOnlySet           bool
	CheckPublishRouting     bool
	CheckRoutingSet         bool
	SkipChecksumComparison  bool
	SkipChecksumSet         bool
	IgnoreOlderAddedInRight bool
	IgnoreOlderAddedSet     bool
}

func compareOnAppInit(_ *azldev.App, parentCmd *cobra.Command) {
	parentCmd.AddCommand(NewCompareCmd())
}

// NewCompareCmd constructs the `azldev repo compare` command.
func NewCompareCmd() *cobra.Command {
	var options CompareOptions

	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare package inventories in two RPM repo sets",
		Long: `Compare package inventories from two configured RPM repo sets.

The report groups differences by RPM package name. Each package entry contains
the left and right NEVR inventories, RPM architectures, logical channels, and
repository-architecture coverage. Package summaries identify inventory,
content, duplicate-publication, routing, and noarch-replication differences.

JSON and Markdown preserve the full package hierarchy. Table and CSV output use
one summary row per differing package name.

Use --comparison to load a named [resources.rpm-repo-comparisons] profile.
--left, --right, --arch, --latest-only, --check-publish-routing, and
--skip-checksum-comparison and --ignore-older-added-in-right override profile values. Without a profile,
--left and --right are required.

Every physical repository's repomd.xml is fetched before any primary metadata,
then each referenced primary file is downloaded into invocation-local memory.
Ambient dnf caches are never read or reused.`,
	}

	cmd.RunE = azldev.RunFunc(func(env *azldev.Env) (interface{}, error) {
		options.LatestOnlySet = cmd.Flags().Changed("latest-only")
		options.CheckRoutingSet = cmd.Flags().Changed("check-publish-routing")
		options.SkipChecksumSet = cmd.Flags().Changed("skip-checksum-comparison")
		options.IgnoreOlderAddedSet = cmd.Flags().Changed("ignore-older-added-in-right")

		report, err := RunCompare(env, &options)
		if err != nil {
			return nil, err
		}

		switch env.DefaultReportFormat() {
		case azldev.ReportFormatMarkdown:
			if err := renderComparisonMarkdown(env.ReportFile(), report); err != nil {
				return nil, err
			}

			return true, nil
		case azldev.ReportFormatCSV, azldev.ReportFormatTable:
			return repocompare.SummaryRows(report.Packages), nil
		case azldev.ReportFormatJSON:
			return report, nil
		default:
			return report, nil
		}
	})

	cmd.Flags().StringVar(&options.Comparison, "comparison", "",
		"named [resources.rpm-repo-comparisons] profile")
	cmd.Flags().StringVar(&options.Left, "left", "", "left [resources.rpm-repo-sets] name")
	cmd.Flags().StringVar(&options.Right, "right", "", "right [resources.rpm-repo-sets] name")
	cmd.Flags().StringSliceVar(&options.Arches, "arch", nil,
		"comma-separated target architectures (default: profile value or x86_64,aarch64)")
	cmd.Flags().BoolVar(&options.LatestOnly, "latest-only", false,
		"keep the latest EVR independently per side, physical sub-repo, package name, architecture, and kind")
	cmd.Flags().BoolVar(&options.CheckPublishRouting, "check-publish-routing", true,
		"validate physical placement against project publish-channel metadata")
	cmd.Flags().BoolVar(&options.SkipChecksumComparison, "skip-checksum-comparison", false,
		"skip checksum and size comparison for matching package identities")
	cmd.Flags().BoolVar(&options.IgnoreOlderAddedInRight, "ignore-older-added-in-right", false,
		"ignore right-only identities older than a matching left package identity")

	return cmd
}

// RunCompare loads both repository sets and returns a self-contained difference report.
func RunCompare(env *azldev.Env, options *CompareOptions) (repocompare.Report, error) {
	resolved, err := resolveCompareOptions(env.Config(), options)
	if err != nil {
		return repocompare.Report{}, err
	}

	leftSet := env.Config().Resources.RpmRepoSets[resolved.Left]
	rightSet := env.Config().Resources.RpmRepoSets[resolved.Right]

	leftRepos, leftChannels, err := comparisonRepositories(
		env.Config(), resolved.Left, leftSet, resolved.Arches)
	if err != nil {
		return repocompare.Report{}, fmt.Errorf(
			"resolving left repo set %#q:\n%w", resolved.Left, err)
	}

	rightRepos, rightChannels, err := comparisonRepositories(
		env.Config(), resolved.Right, rightSet, resolved.Arches)
	if err != nil {
		return repocompare.Report{}, fmt.Errorf(
			"resolving right repo set %#q:\n%w", resolved.Right, err)
	}

	leftRepos, rightRepos = filterToSharedKinds(leftRepos, rightRepos)
	if len(leftRepos) == 0 || len(rightRepos) == 0 {
		return repocompare.Report{},
			errors.New("the selected repo sets have no shared artifact kinds")
	}

	if leftSet.DisableSSLVerify || rightSet.DisableSSLVerify {
		slog.Warn("TLS certificate verification is disabled for one or more comparison inputs")
	}

	fetcher := &repocompare.HTTPFetcher{Attempts: env.NetworkRetries()}

	leftPackages, leftSnapshots, err := repocompare.LoadRepositories(env, fetcher, leftRepos)
	if err != nil {
		return repocompare.Report{}, fmt.Errorf("loading left repositories:\n%w", err)
	}

	rightPackages, rightSnapshots, err := repocompare.LoadRepositories(env, fetcher, rightRepos)
	if err != nil {
		return repocompare.Report{}, fmt.Errorf("loading right repositories:\n%w", err)
	}

	compareOptions := repocompare.Options{
		LatestOnly:              resolved.LatestOnly,
		SkipChecksumComparison:  resolved.SkipChecksumComparison,
		IgnoreOlderAddedInRight: resolved.IgnoreOlderAddedInRight,
		CheckPublishRouting:     resolved.CheckPublishRouting,
		LeftUnrouted:            leftSet.Unrouted,
		RightUnrouted:           rightSet.Unrouted,
		LeftChannels:            leftChannels,
		RightChannels:           rightChannels,
		ResolveChannel:          publishChannelResolver(env.Config()),
	}

	packages, err := repocompare.BuildPackageReports(leftPackages, rightPackages, compareOptions)
	if err != nil {
		return repocompare.Report{}, fmt.Errorf("comparing repository inventories:\n%w", err)
	}

	return newComparisonReport(resolved, leftSnapshots, rightSnapshots, packages), nil
}

func newComparisonReport(
	resolved projectconfig.RpmRepoComparison,
	leftSnapshots, rightSnapshots []repocompare.Snapshot,
	packages []repocompare.PackageReport,
) repocompare.Report {
	sort.Slice(leftSnapshots, func(i, j int) bool {
		return leftSnapshots[i].RepoID < leftSnapshots[j].RepoID
	})
	sort.Slice(rightSnapshots, func(i, j int) bool {
		return rightSnapshots[i].RepoID < rightSnapshots[j].RepoID
	})

	checksumComparison := "compared"
	if resolved.SkipChecksumComparison {
		checksumComparison = "skipped"
	}

	return repocompare.Report{
		Comparison: repocompare.ComparisonMetadata{
			Left:                    resolved.Left,
			Right:                   resolved.Right,
			Architectures:           append([]string(nil), resolved.Arches...),
			LatestOnly:              resolved.LatestOnly,
			ChecksumComparison:      checksumComparison,
			IgnoreOlderAddedInRight: resolved.IgnoreOlderAddedInRight,
		},
		Summary: repocompare.SummarizeReports(packages),
		Snapshots: repocompare.ReportSnapshots{
			Left:  leftSnapshots,
			Right: rightSnapshots,
		},
		Packages: packages,
	}
}

func resolveCompareOptions(
	cfg *projectconfig.ProjectConfig,
	options *CompareOptions,
) (projectconfig.RpmRepoComparison, error) {
	resolved := projectconfig.RpmRepoComparison{
		Arches:              append([]string(nil), repolayout.DefaultArches...),
		CheckPublishRouting: true,
	}

	if options.Comparison != "" {
		profile, ok := cfg.Resources.RpmRepoComparisons[options.Comparison]
		if !ok {
			return resolved, fmt.Errorf(
				"rpm-repo-comparison %#q is not defined", options.Comparison)
		}

		resolved = profile
		if len(resolved.Arches) == 0 {
			resolved.Arches = append([]string(nil), repolayout.DefaultArches...)
		}
	}

	if options.Left != "" {
		resolved.Left = options.Left
	}

	if options.Right != "" {
		resolved.Right = options.Right
	}

	if len(options.Arches) > 0 {
		resolved.Arches = append([]string(nil), options.Arches...)
	}

	applyComparisonBooleanOverrides(&resolved, options)

	if resolved.Left == "" || resolved.Right == "" {
		return resolved, errors.New(
			"set both '--left' and '--right', or select a profile with '--comparison'")
	}

	if resolved.Left == resolved.Right {
		return resolved, errors.New("'--left' and '--right' must name different repo sets")
	}

	if _, ok := cfg.Resources.RpmRepoSets[resolved.Left]; !ok {
		return resolved, fmt.Errorf("left rpm-repo-set %#q is not defined", resolved.Left)
	}

	if _, ok := cfg.Resources.RpmRepoSets[resolved.Right]; !ok {
		return resolved, fmt.Errorf("right rpm-repo-set %#q is not defined", resolved.Right)
	}

	return resolved, nil
}

func applyComparisonBooleanOverrides(
	resolved *projectconfig.RpmRepoComparison,
	options *CompareOptions,
) {
	if options.LatestOnlySet {
		resolved.LatestOnly = options.LatestOnly
	}

	if options.CheckRoutingSet {
		resolved.CheckPublishRouting = options.CheckPublishRouting
	}

	if options.SkipChecksumSet {
		resolved.SkipChecksumComparison = options.SkipChecksumComparison
	}

	if options.IgnoreOlderAddedSet {
		resolved.IgnoreOlderAddedInRight = options.IgnoreOlderAddedInRight
	}
}

func comparisonRepositories(
	cfg *projectconfig.ProjectConfig,
	setName string,
	set projectconfig.RpmRepoSet,
	arches []string,
) ([]repocompare.Repository, map[string][]string, error) {
	tmpl, ok := cfg.Resources.RpmRepoSetTemplates[set.Template]
	if !ok {
		return nil, nil, fmt.Errorf("template %#q is not defined", set.Template)
	}

	allow := make(map[string]struct{}, len(set.Subrepos))
	for _, name := range set.Subrepos {
		allow[name] = struct{}{}
	}

	channels := make(map[string][]string)
	repositories := make([]repocompare.Repository, 0, len(tmpl.Subrepos)*len(arches))

	for _, subrepo := range tmpl.Subrepos {
		if len(allow) > 0 {
			if _, ok := allow[subrepo.Name]; !ok {
				continue
			}
		}

		channels[subrepo.Name] = append([]string(nil), subrepo.PublishChannels...)
		if strings.Contains(subrepo.Subpath, "$basearch") {
			for _, arch := range arches {
				if !setAvailableForArch(set.Arches, arch) {
					continue
				}

				repositories = append(repositories, repocompare.Repository{
					ID:               setName + "-" + subrepo.Name + "-" + arch,
					Subrepo:          subrepo.Name,
					Arch:             arch,
					Kind:             subrepo.Kind.Default(),
					URL:              joinRepoURL(set.BaseURI, subrepo.Subpath, arch),
					PublishChannels:  append([]string(nil), subrepo.PublishChannels...),
					Unrouted:         set.Unrouted,
					DisableSSLVerify: set.DisableSSLVerify,
				})
			}
		} else {
			repositories = append(repositories, repocompare.Repository{
				ID:               setName + "-" + subrepo.Name,
				Subrepo:          subrepo.Name,
				Kind:             subrepo.Kind.Default(),
				URL:              joinRepoURL(set.BaseURI, subrepo.Subpath, ""),
				PublishChannels:  append([]string(nil), subrepo.PublishChannels...),
				Unrouted:         set.Unrouted,
				DisableSSLVerify: set.DisableSSLVerify,
			})
		}
	}

	return repositories, channels, nil
}

func joinRepoURL(baseURI, subpath, arch string) string {
	subpath = strings.ReplaceAll(subpath, "$basearch", arch)

	return strings.TrimRight(baseURI, "/") + "/" + strings.TrimLeft(subpath, "/")
}

func setAvailableForArch(allowlist []string, arch string) bool {
	if len(allowlist) == 0 {
		return true
	}

	for _, allowed := range allowlist {
		if arch == allowed {
			return true
		}
	}

	return false
}

func filterToSharedKinds(
	left, right []repocompare.Repository,
) ([]repocompare.Repository, []repocompare.Repository) {
	leftKinds := make(map[projectconfig.SubrepoKind]struct{})
	rightKinds := make(map[projectconfig.SubrepoKind]struct{})

	for _, repo := range left {
		leftKinds[repo.Kind] = struct{}{}
	}

	for _, repo := range right {
		rightKinds[repo.Kind] = struct{}{}
	}

	filter := func(
		repositories []repocompare.Repository,
		other map[projectconfig.SubrepoKind]struct{},
	) []repocompare.Repository {
		result := make([]repocompare.Repository, 0, len(repositories))
		for _, repository := range repositories {
			if _, ok := other[repository.Kind]; ok {
				result = append(result, repository)
			}
		}

		return result
	}

	return filter(left, rightKinds), filter(right, leftKinds)
}

func publishChannelResolver(cfg *projectconfig.ProjectConfig) repocompare.ChannelResolver {
	cache := make(map[string]string)

	return func(pkg repocompare.Package) (string, error) {
		componentName := sourceComponentName(pkg, cfg)

		cacheKey := strings.Join([]string{componentName, pkg.Name, string(pkg.Kind)}, "\x00")
		if channel, ok := cache[cacheKey]; ok {
			return channel, nil
		}

		component := cfg.Components[componentName]
		if component.Name == "" {
			component.Name = componentName
		}

		resolved, err := projectconfig.ResolveComponentConfig(
			component,
			cfg.DefaultComponentConfig,
			projectconfig.ComponentConfig{},
			cfg.ComponentGroups,
			cfg.GroupsByComponent[componentName],
		)
		if err != nil {
			return "", fmt.Errorf("resolving component %#q:\n%w", componentName, err)
		}

		if pkg.Kind == projectconfig.SubrepoKindSource {
			cache[cacheKey] = resolved.Publish.SRPMChannel

			return resolved.Publish.SRPMChannel, nil
		}

		channel, err := projectconfig.ResolvePackagePublishChannel(pkg.Name, &resolved, cfg)
		if err != nil {
			return "", fmt.Errorf("resolving package publish channel:\n%w", err)
		}

		cache[cacheKey] = channel

		return channel, nil
	}
}

func sourceComponentName(pkg repocompare.Package, cfg *projectconfig.ProjectConfig) string {
	if pkg.Kind == projectconfig.SubrepoKindSource {
		return pkg.Name
	}

	sourceRPM := strings.TrimSuffix(pkg.SourceRPM, ".rpm")
	sourceRPM = strings.TrimSuffix(sourceRPM, ".src")
	sourceRPM = strings.TrimSuffix(sourceRPM, ".nosrc")

	candidates := make([]string, 0)

	for name := range cfg.Components {
		if strings.HasPrefix(sourceRPM, name+"-") {
			candidates = append(candidates, name)
		}
	}

	if len(candidates) == 0 {
		return pkg.Name
	}

	sort.Slice(candidates, func(i, j int) bool {
		return len(candidates[i]) > len(candidates[j])
	})

	return candidates[0]
}
