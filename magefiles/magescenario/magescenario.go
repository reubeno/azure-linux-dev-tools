// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package magescenario

import (
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/buildtestenv"
	"github.com/microsoft/azure-linux-dev-tools/magefiles/magebuild"
	"github.com/microsoft/azure-linux-dev-tools/magefiles/mageutil"
)

var ErrScenario = errors.New("scenario test failed")

const e2eTargetAll = "all"

// Scenario runs the scenario tests.
func Scenario() error {
	startTime := time.Now()

	mageutil.MagePrintln(mageutil.MsgStart, "Starting scenario tests...")

	mg.SerialDeps(mg.F(mageScenarioCommon, false))

	mageutil.MagePrintf(mageutil.MsgSuccess, "Scenario tests complete: %v.\n", time.Since(startTime))

	return nil
}

// ScenarioUpdate updates snapshots and runs the scenario tests.
func ScenarioUpdate() error {
	mageutil.MagePrintln(mageutil.MsgStart, "Updating scenario snapshots...")

	mg.SerialDeps(mg.F(mageScenarioCommon, true))

	mageutil.MagePrintln(mageutil.MsgSuccess, "Scenario snapshots updated.")

	return nil
}

// E2E runs all, component, image, or frameworks tests (default: all).
// Use -run instead of -target for a focused Go test/subtest expression.
// These use the 'e2e' tag and do not run as part of 'mage scenario' or 'mage all'.
func E2E(target, run *string) error {
	startTime := time.Now()

	options, err := e2eOptions(target, run)
	if err != nil {
		return err
	}

	mageutil.MagePrintf(mageutil.MsgStart, "Starting %s tests...\n", options.label)

	if err := runScenarioGoTest(options); err != nil {
		return err
	}

	mageutil.MagePrintf(mageutil.MsgSuccess, "End-to-end tests complete: %v.\n", time.Since(startTime))

	return nil
}

func e2eOptions(target, run *string) (scenarioGoTestOptions, error) {
	selected := e2eTargetAll
	if target != nil {
		selected = *target
	}

	options := scenarioGoTestOptions{label: "end-to-end", buildTag: "e2e", timeout: "90m", packagePattern: "./scenario"}

	switch selected {
	case e2eTargetAll:
	case "component":
		options.label, options.testSelector = "component end-to-end", "^TestAzureLinuxComponent"
	case "image":
		options.label, options.testSelector, options.timeout = "image-build end-to-end", "^TestAzureLinuxImageBuild$", "25m"
	case "frameworks":
		options.label, options.testSelector = "native framework contracts", "^TestNativeFrameworkContracts$"
		options.timeout = "60m"
	default:
		return options, fmt.Errorf("%w: unknown end-to-end target %#q; available targets: %q",
			ErrScenario, selected, []string{e2eTargetAll, "component", "image", "frameworks"})
	}

	if run != nil {
		if selected != e2eTargetAll || *run == "" {
			return options, fmt.Errorf("%w: use a nonempty '-run' expression instead of a named '-target'", ErrScenario)
		}

		if _, err := regexp.Compile(*run); err != nil {
			return options, fmt.Errorf("invalid end-to-end '-run' expression:\n%w", err)
		}

		options.testSelector = *run
	}

	return options, nil
}

func mageScenarioCommon(doSnapshotUpdate bool) error {
	options := scenarioGoTestOptions{
		label:            "scenario",
		buildTag:         "scenario",
		timeout:          "60m",
		packagePattern:   "./scenario/...",
		doSnapshotUpdate: doSnapshotUpdate,
	}

	if doSnapshotUpdate {
		mageutil.MagePrintln(mageutil.MsgInfo, "Will update snapshots.")
	}

	return runScenarioGoTest(options)
}

// scenarioGoTestOptions parameterizes the shared 'go test' invocation used by
// both [Scenario] / [ScenarioUpdate] and [E2E]. They run identical
// build/test pipelines but differ in build tag, timeout, package pattern, and
// snapshot-update behavior.
type scenarioGoTestOptions struct {
	// label is a short human-readable name for the tier ("scenario" or
	// "end-to-end") used in progress and error messages so that CI logs
	// clearly identify which suite is running/failed.
	label string
	// buildTag is the Go build tag passed via '-tags='. Files in the scenario
	// package have either '//go:build scenario' or '//go:build e2e' (with
	// 'setup_test.go' widened to 'scenario || e2e' so [TestMain] is shared).
	buildTag string
	// timeout is the value passed via '-timeout=' to 'go test'.
	timeout string
	// packagePattern is the package selector passed to 'go test', e.g.
	// './scenario' or './scenario/...'.
	packagePattern string
	// doSnapshotUpdate, when true, sets the env var that puts the snapshot
	// helper into update mode. Only meaningful for the scenario tier.
	doSnapshotUpdate bool
	// testSelector, when set, is passed to 'go test -run' to execute one
	// focused test without running the rest of the selected tier.
	testSelector string
}

func runScenarioGoTest(options scenarioGoTestOptions) error {
	mg.SerialDeps(magebuild.Build)

	mageutil.MagePrintf(mageutil.MsgStart, "Running %s tests...\n", options.label)

	//nolint:lll // Can't really break up the long URI.
	// Add workaround for go-snaps disablement of color based on $_.
	// Reference: https://github.com/gkampitakis/go-snaps/blob/e004bc15e5166169d41a9d082967275fffb9d2b4/internal/colors/colors.go#L35
	if os.Getenv(mageutil.MageColorEnableEnvVar) == "true" {
		os.Setenv("_", "")
	}

	// mage knows the exact path to the azldev binary, ensure we use it.
	env := map[string]string{
		buildtestenv.TestingAzldevBinPathEnvVar: path.Join(mageutil.BinDir(), "azldev"),
	}

	if options.doSnapshotUpdate {
		env[buildtestenv.TestingUpdateSnapshotsEnvVar] = buildtestenv.TestingUpdateSnapshotsEnvValue
	}

	// '-count=1' causes the cache to be ignored for scenario tests so they always re-run (it would only rebuild if
	// we changed the test code itself, not the application code).
	args := []string{
		"test",
		"-tags=" + options.buildTag,
		"-timeout=" + options.timeout,
		"-count=1",
	}
	if options.testSelector != "" {
		args = append(args, "-run="+options.testSelector)
	}

	args = append(args, options.packagePattern)

	output, err := sh.OutputWith(env, mg.GoCmd(), args...)
	if err != nil {
		titledLabel := strings.ToUpper(options.label[:1]) + options.label[1:]
		mageutil.MagePrintf(mageutil.MsgError, "%s test failed; details follow.\n", titledLabel)

		for _, line := range strings.Split(output, "\n") {
			mageutil.MagePrintln(mageutil.MsgInfo, line)
		}

		return mageutil.PrintAndReturnError(titledLabel+" test failed (see diagnostic output above).", ErrScenario, err)
	}

	return nil
}
