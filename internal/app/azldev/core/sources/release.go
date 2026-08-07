// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sources

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/rpm/spec"
)

// autoreleasePattern matches the %autorelease macro invocation in a Release tag value.
// This covers:
//   - bare form: %autorelease
//   - braced form: %{autorelease}
//   - braced form with arguments: %{autorelease -e asan}
//   - conditional form (no fallback): %{?autorelease}
var autoreleasePattern = regexp.MustCompile(`%(\{[?]?autorelease($|[}\s])|autorelease($|\s))`)

// defaultReleaseCounterRegex models the built-in static release calculation.
// Projects may replace it through default-component-config.release.counter.
const defaultReleaseCounterRegex = `^(\d+)(?:%\{\??dist\})?$`

var staticReleasePattern = regexp.MustCompile(defaultReleaseCounterRegex)

var errReleaseCounterNoMatch = errors.New("release counter regex did not match")

// GetReleaseTagValue reads the Release tag value from the spec file at specPath.
// It returns the raw value string as written in the spec (e.g. "1%{?dist}" or "%autorelease").
// Returns [spec.ErrNoSuchTag] if no Release tag is found.
func GetReleaseTagValue(fs opctx.FS, specPath string) (string, error) {
	releaseValues, err := GetReleaseTagValues(fs, specPath)
	if err != nil {
		return "", err
	}

	if len(releaseValues) > 1 {
		return "", fmt.Errorf(
			"spec %#q contains %d main-package Release tags; expected exactly one",
			specPath, len(releaseValues),
		)
	}

	return releaseValues[0], nil
}

// GetReleaseTagValues reads every main-package Release tag value from the spec.
// Conditional specs may contain more than one physical Release tag.
func GetReleaseTagValues(fs opctx.FS, specPath string) ([]string, error) {
	specFile, err := fs.Open(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open spec %#q:\n%w", specPath, err)
	}
	defer specFile.Close()

	openedSpec, err := spec.OpenSpec(specFile)
	if err != nil {
		return nil, fmt.Errorf("failed to parse spec %#q:\n%w", specPath, err)
	}

	var releaseValues []string

	err = openedSpec.VisitTagsPackage("", func(tagLine *spec.TagLine, _ *spec.Context) error {
		if strings.EqualFold(tagLine.Tag, "Release") {
			releaseValues = append(releaseValues, tagLine.Value)
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to visit tags in spec %#q:\n%w", specPath, err)
	}

	if len(releaseValues) == 0 {
		return nil, fmt.Errorf("release tag not found in spec %#q:\n%w", specPath, spec.ErrNoSuchTag)
	}

	return releaseValues, nil
}

// ReleaseUsesAutorelease reports whether the given Release tag value uses the
// %autorelease macro (either bare or braced form).
func ReleaseUsesAutorelease(releaseValue string) bool {
	return autoreleasePattern.MatchString(releaseValue)
}

// BumpStaticRelease increments the built-in static Release counter by the given commit count.
func BumpStaticRelease(releaseValue string, commitCount int) (string, error) {
	return BumpReleaseWithRegex(releaseValue, staticReleasePattern.String(), commitCount)
}

// BumpReleaseWithRegex increments the single captured integral counter in releaseValue.
// The expression must match the full Release value and contain exactly one capturing group.
func BumpReleaseWithRegex(releaseValue, pattern string, commitCount int) (string, error) {
	if commitCount < 0 {
		return "", fmt.Errorf("release bump count must not be negative: %d", commitCount)
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("failed to compile release counter regex %#q:\n%w", pattern, err)
	}

	if compiled.NumSubexp() != 1 {
		return "", fmt.Errorf(
			"release counter regex %#q must contain exactly one capturing group; found %d",
			pattern, compiled.NumSubexp(),
		)
	}

	matches := compiled.FindAllStringSubmatchIndex(releaseValue, -1)
	if len(matches) != 1 || matches[0][0] != 0 || matches[0][1] != len(releaseValue) {
		return "", fmt.Errorf(
			"release counter regex %#q must match the full Release value %#q exactly once: %w",
			pattern, releaseValue, errReleaseCounterNoMatch,
		)
	}

	captureStart := matches[0][2]

	captureEnd := matches[0][3]
	if captureStart < 0 || captureEnd < 0 {
		return "", fmt.Errorf(
			"release counter regex %#q did not capture a counter from Release value %#q",
			pattern, releaseValue,
		)
	}

	return incrementCapturedCounter(releaseValue, captureStart, captureEnd, commitCount)
}

func incrementCapturedCounter(value string, start, end, increment int) (string, error) {
	counter := value[start:end]
	for _, char := range counter {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("captured release counter %#q is not an unsigned decimal integer", counter)
		}
	}

	currentRelease, err := strconv.Atoi(counter)
	if err != nil {
		return "", fmt.Errorf("failed to parse release counter %#q:\n%w", counter, err)
	}

	newCounter := strconv.Itoa(currentRelease + increment)
	if len(newCounter) < len(counter) {
		newCounter = strings.Repeat("0", len(counter)-len(newCounter)) + newCounter
	}

	return value[:start] + newCounter + value[end:], nil
}

// tryBumpStaticRelease manages release calculation based on the component's release
// calculation mode. It may bump, skip, or auto-detect depending on configuration:
//
//   - "manual":      no-op — component manages its own release numbering.
//   - "autorelease": no-op — rpmautospec resolves the release from git history.
//   - "static":      always bumps the configured or built-in integral counter.
//   - "auto":        auto-detects from the spec's Release tag value; skips if
//     %autorelease is found, otherwise uses the configured or built-in counter.
func (p *sourcePreparerImpl) tryBumpStaticRelease(
	component components.Component,
	sourcesDirPath string,
	commitCount int,
) error {
	releaseConfig := component.GetConfig().Release
	if err := releaseConfig.Validate(); err != nil {
		return fmt.Errorf("component %#q has invalid release configuration:\n%w",
			component.GetName(), err)
	}

	switch releaseConfig.Calculation {
	case projectconfig.ReleaseCalculationManual:
		slog.Debug("Component uses manual release calculation; skipping static release bump",
			"component", component.GetName())

		return nil

	case projectconfig.ReleaseCalculationAutorelease:
		slog.Debug("Component uses autorelease calculation; skipping static release bump",
			"component", component.GetName())

		return nil

	case projectconfig.ReleaseCalculationStatic:
		return p.readAndBumpRelease(component, sourcesDirPath, commitCount, true)

	case projectconfig.ReleaseCalculationAuto:
		return p.readAndBumpRelease(component, sourcesDirPath, commitCount, false)

	default:
		return fmt.Errorf("component %#q has unknown release calculation mode %#q",
			component.GetName(), releaseConfig.Calculation)
	}
}

// readAndBumpRelease reads the Release tag and applies the selected static counter strategy.
// When requireStaticRelease is true (explicit static mode), encountering %autorelease
// produces an error telling the user to switch to 'release.calculation = "autorelease"'.
// When false (auto mode), specs using %autorelease are silently skipped.
func (p *sourcePreparerImpl) readAndBumpRelease(
	component components.Component,
	sourcesDirPath string,
	commitCount int,
	requireStaticRelease bool,
) error {
	specPath, err := p.resolveSpecPath(component, sourcesDirPath)
	if err != nil {
		return err
	}

	releaseValues, err := GetReleaseTagValues(p.fs, specPath)
	if err != nil {
		return fmt.Errorf("failed to read Release tag for component %#q:\n%w",
			component.GetName(), err)
	}

	usesAutorelease := false
	for _, releaseValue := range releaseValues {
		usesAutorelease = usesAutorelease || ReleaseUsesAutorelease(releaseValue)
	}

	if usesAutorelease {
		if requireStaticRelease {
			return fmt.Errorf(
				"component %#q has 'release.calculation = \"static\"' but a Release tag "+
					"uses %%autorelease; set 'release.calculation = \"autorelease\"' instead",
				component.GetName())
		}

		slog.Debug("Spec uses %%autorelease; skipping static release bump",
			"component", component.GetName())

		return nil
	}

	counterConfig := component.GetConfig().Release.Counter
	if counterConfig != nil && counterConfig.Source == projectconfig.ReleaseCounterSourceSpecMacro {
		return p.bumpSpecMacroCounter(component, specPath, commitCount, counterConfig)
	}

	counterRegex := defaultReleaseCounterRegex
	if counterConfig != nil {
		counterRegex = counterConfig.Regex
	}

	if err := p.bumpReleaseTagCounter(component, specPath, commitCount, counterRegex); err != nil {
		return fmt.Errorf(
			"component %#q cannot be bumped with release counter regex %#q; "+
				"configure 'release.counter' or set 'release.calculation = \"manual\"':\n%w",
			component.GetName(), counterRegex, err,
		)
	}

	return nil
}

func (p *sourcePreparerImpl) bumpReleaseTagCounter(
	component components.Component,
	specPath string,
	commitCount int,
	counterRegex string,
) error {
	content, err := p.fs.Open(specPath)
	if err != nil {
		return fmt.Errorf("failed to open spec %#q:\n%w", specPath, err)
	}

	openedSpec, err := spec.OpenSpec(content)
	content.Close()
	if err != nil {
		return fmt.Errorf("failed to parse spec %#q:\n%w", specPath, err)
	}

	var (
		oldLine    string
		newLine    string
		oldRelease string
		newRelease string
		matchCount int
	)

	err = openedSpec.VisitTagsPackage("", func(tagLine *spec.TagLine, ctx *spec.Context) error {
		if !strings.EqualFold(tagLine.Tag, "Release") {
			return nil
		}

		bumped, bumpErr := BumpReleaseWithRegex(tagLine.Value, counterRegex, commitCount)
		if bumpErr != nil {
			if errors.Is(bumpErr, errReleaseCounterNoMatch) {
				return nil
			}

			return bumpErr
		}

		matchCount++
		if matchCount == 1 {
			oldLine = *ctx.RawLine
			newLine = fmt.Sprintf("%s: %s", tagLine.Tag, bumped)
			oldRelease = tagLine.Value
			newRelease = bumped
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to inspect Release tags for component %#q:\n%w",
			component.GetName(), err)
	}

	if matchCount != 1 {
		return fmt.Errorf(
			"component %#q release counter regex %#q matched %d main-package Release tags; expected exactly one",
			component.GetName(), counterRegex, matchCount,
		)
	}

	slog.Info("Bumping static release",
		"component", component.GetName(),
		"counterSource", projectconfig.ReleaseCounterSourceReleaseTag,
		"oldRelease", oldRelease,
		"newRelease", newRelease,
		"commitCount", commitCount)

	overlay := projectconfig.ComponentOverlay{
		Type:        projectconfig.ComponentOverlaySearchAndReplaceInSpec,
		Regex:       "^" + regexp.QuoteMeta(oldLine) + "$",
		Replacement: newLine,
	}

	if err := ApplySpecOverlayToFileInPlace(p.fs, overlay, specPath); err != nil {
		return fmt.Errorf("failed to apply release bump overlay for component %#q:\n%w",
			component.GetName(), err)
	}

	return nil
}

func (p *sourcePreparerImpl) bumpSpecMacroCounter(
	component components.Component,
	specPath string,
	commitCount int,
	counterConfig *projectconfig.ReleaseCounterConfig,
) error {
	content, err := p.fs.Open(specPath)
	if err != nil {
		return fmt.Errorf("failed to open spec %#q:\n%w", specPath, err)
	}

	openedSpec, err := spec.OpenSpec(content)
	content.Close()

	if err != nil {
		return fmt.Errorf("failed to parse spec %#q:\n%w", specPath, err)
	}

	matchedLine, newLine, matchCount, err := findSpecMacroCounterDefinition(
		openedSpec, counterConfig, commitCount,
	)
	if err != nil {
		return fmt.Errorf("failed to inspect release counter macro for component %#q:\n%w",
			component.GetName(), err)
	}

	if matchCount != 1 {
		return fmt.Errorf(
			"component %#q release counter expected exactly one bare integral %%%s %s definition; found %d",
			component.GetName(), counterConfig.Directive, counterConfig.Name, matchCount,
		)
	}

	slog.Info("Bumping static release macro",
		"component", component.GetName(),
		"counterSource", projectconfig.ReleaseCounterSourceSpecMacro,
		"directive", counterConfig.Directive,
		"macro", counterConfig.Name,
		"oldDefinition", matchedLine,
		"newDefinition", newLine,
		"commitCount", commitCount)

	overlay := projectconfig.ComponentOverlay{
		Type:        projectconfig.ComponentOverlaySearchAndReplaceInSpec,
		Regex:       "^" + regexp.QuoteMeta(matchedLine) + "$",
		Replacement: newLine,
	}

	if err := ApplySpecOverlayToFileInPlace(p.fs, overlay, specPath); err != nil {
		return fmt.Errorf("failed to apply release macro bump for component %#q:\n%w",
			component.GetName(), err)
	}

	return nil
}

func findSpecMacroCounterDefinition(
	openedSpec *spec.Spec,
	counterConfig *projectconfig.ReleaseCounterConfig,
	commitCount int,
) (matchedLine, newLine string, matchCount int, err error) {
	macroPattern := regexp.MustCompile(
		`^\s*%` + regexp.QuoteMeta(string(counterConfig.Directive)) + `\s+` +
			regexp.QuoteMeta(counterConfig.Name) + `\s+([0-9]+)\s*$`,
	)

	err = openedSpec.Visit(func(ctx *spec.Context) error {
		if ctx.RawLine == nil {
			return nil
		}

		matches := macroPattern.FindStringSubmatchIndex(*ctx.RawLine)
		if matches == nil {
			return nil
		}

		matchCount++
		if matchCount > 1 {
			return nil
		}

		matchedLine = *ctx.RawLine
		newLine, err = incrementCapturedCounter(matchedLine, matches[2], matches[3], commitCount)

		return err
	})

	return matchedLine, newLine, matchCount, err
}
