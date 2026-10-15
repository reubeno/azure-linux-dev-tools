// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package testexecution orchestrates configured tests without embedding framework-specific execution logic.
package testexecution

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/lisa"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/pytest"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/tmt"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
)

type Subject struct {
	Name         string
	Kind         string
	Distro       projectconfig.DistroVersionDefinition
	Capabilities []string
	Properties   map[string]string
}

const (
	imageSubject     = "image"
	componentSubject = "component"
	tmtFramework     = "tmt"
	lisaFramework    = "lisa"
	pytestFramework  = "pytest"
)

type plannedTest struct {
	test         projectconfig.ResolvedTest
	runner       testframework.Runner
	execution    testframework.ExecutionSpec
	requirements testframework.Requirements
	baseName     string
}

// Select expands and deduplicates either explicit selectors or the subject's associated tests.
func Select(cfg *projectconfig.ProjectConfig, associated []projectconfig.ResolvedTest,
	selectors []string,
) ([]projectconfig.ResolvedTest, error) {
	tests := associated

	if len(selectors) > 0 {
		var err error

		tests, err = cfg.ResolveTestSelectors(selectors)
		if err != nil {
			return nil, fmt.Errorf("select:\n%w", err)
		}
	}

	seen := make(map[string]bool)

	result := make([]projectconfig.ResolvedTest, 0, len(tests))
	for _, test := range tests {
		if !seen[test.Name] {
			result = append(result, test)
			seen[test.Name] = true
		}
	}

	if len(result) == 0 {
		return nil, errors.New("no tests selected for this subject")
	}

	return result, nil
}

// Run plans every selected test before acquiring artifacts, then retains results even when a test fails.
func Run(env *azldev.Env, subject Subject, tests []projectconfig.ResolvedTest, options Options,
	resolver *artifacts.Resolver,
) ([]testframework.RunResult, error) {
	if err := normalizeOptions(env, &options); err != nil {
		return nil, fmt.Errorf("run:\n%w", err)
	}

	plans, err := planTests(subject, tests, options)
	if err != nil {
		return nil, fmt.Errorf("run:\n%w", err)
	}

	inputs, err := resolveInputs(subject, plans, options, resolver)
	if err != nil {
		return nil, fmt.Errorf("run:\n%w", err)
	}

	runDir := filepath.Join(options.WorkDir, "tests", "dry-run")
	if !env.DryRun() {
		root := filepath.Join(options.WorkDir, "tests")
		if err := fileutils.MkdirAll(env.FS(), root); err != nil {
			return nil, fmt.Errorf("run:\n%w", err)
		}

		runDir, err = fileutils.MkdirTemp(env.FS(), root, "run-")
		if err != nil {
			return nil, fmt.Errorf("run:\n%w", err)
		}
	}

	results := make([]testframework.RunResult, 0, len(plans))

	var failures []error
	for index, plan := range plans {
		result, err := executeOne(env, subject, plan, inputs[index], options, runDir, len(failures) > 0)

		results = append(results, result)
		if err != nil {
			failures = append(failures, fmt.Errorf("test %#q:\n%w", plan.test.Name, err))
		}
	}

	if !env.DryRun() {
		if err := saveResults(env, runDir, options.JUnitXMLPath, results); err != nil {
			failures = append(failures, err)
		}
	}

	return results, errors.Join(failures...)
}

func normalizeOptions(env *azldev.Env, options *Options) error {
	if options.Arch == "" {
		options.Arch = qemu.GoArchToQEMUArch(runtime.GOARCH)
	}

	if !slices.Contains(projectconfig.SupportedImageArchitectures(), options.Arch) {
		return fmt.Errorf("unsupported test architecture %#q", options.Arch)
	}

	if options.WorkDir == "" {
		options.WorkDir = env.WorkDir()
	}

	if options.WorkDir == "" {
		return errors.New("test execution requires a project work directory")
	}

	paths := []*string{&options.WorkDir, &options.SourceDir, &options.LisaDir, &options.SSHKey, &options.JUnitXMLPath}
	for _, path := range paths {
		if *path != "" && !filepath.IsAbs(*path) {
			*path = filepath.Join(env.ProjectDir(), *path)
		}
	}

	for _, source := range []string{options.SourceDir, options.LisaDir} {
		if source != "" && (containsDir(source, options.WorkDir) || containsDir(options.WorkDir, source)) {
			return errors.New("test source and work directories must not overlap")
		}
	}

	return nil
}

func containsDir(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func planTests(subject Subject, tests []projectconfig.ResolvedTest, options Options) ([]plannedTest, error) {
	if err := options.subjectSelection(subject.Kind).Validate(); err != nil {
		return nil, fmt.Errorf("plan tests:\n%w", err)
	}

	if err := options.baseSelection().Validate(); err != nil {
		return nil, fmt.Errorf("plan tests:\n%w", err)
	}

	if len(tests) == 0 {
		return nil, errors.New("no tests selected")
	}

	if err := validateOverrides(tests, options); err != nil {
		return nil, err
	}

	plans := make([]plannedTest, 0, len(tests))
	hasBaseConsumer := false

	for _, test := range tests {
		plan, err := planOne(subject, test, options)
		if err != nil {
			return nil, fmt.Errorf("test %#q:\n%w", test.Name, err)
		}

		plans = append(plans, plan)
		hasBaseConsumer = hasBaseConsumer || plan.requirements.ImageRequired
	}

	if options.hasBaseOptions() && (subject.Kind != componentSubject || !hasBaseConsumer) {
		return nil, errors.New("base image options require a component test that consumes a supporting image")
	}

	return plans, nil
}

func planOne(subject Subject, test projectconfig.ResolvedTest, options Options) (plannedTest, error) {
	if err := testframework.ValidateName(test.Name); err != nil {
		return plannedTest{}, fmt.Errorf("plan one:\n%w", err)
	}

	runner, err := newRunner(test)
	if err != nil {
		return plannedTest{}, fmt.Errorf("plan one:\n%w", err)
	}

	strategy := executionStrategy(test, options.Strategy)

	execution := executionFor(test, options, strategy)
	if err := execution.Validate(); err != nil {
		return plannedTest{}, fmt.Errorf("plan one:\n%w", err)
	}

	requirements, err := runner.Requirements(execution)
	if err != nil {
		return plannedTest{}, fmt.Errorf("plan one:\n%w", err)
	}

	if subject.Kind == imageSubject && len(requirements.ImageFormats) == 0 {
		return plannedTest{}, errors.New("this execution strategy does not consume the image under test")
	}

	if subject.Kind == componentSubject && !requirements.PackagesSupported {
		return plannedTest{}, errors.New("this framework/strategy cannot consume candidate component packages")
	}

	baseName, err := selectBaseName(subject, test, requirements, options, strategy)
	if err != nil {
		return plannedTest{}, fmt.Errorf("plan one:\n%w", err)
	}

	return plannedTest{
		test: test, runner: runner, execution: execution, requirements: requirements, baseName: baseName,
	}, nil
}

func newRunner(test projectconfig.ResolvedTest) (testframework.Runner, error) {
	var (
		runner testframework.Runner
		err    error
	)

	switch test.Definition.Type {
	case tmtFramework:
		runner, err = tmt.New(test.Name, test.Definition.Tmt)
	case lisaFramework:
		runner, err = lisa.New(test.Name, test.Definition.Lisa)
	case pytestFramework:
		raw := make(map[string]any, len(test.Definition.Pytest))
		for key, value := range test.Definition.Pytest {
			raw[key] = value
		}

		if _, source := raw["source"]; !source {
			raw["working-dir"] = test.Definition.PytestWorkingDir()
		}

		runner, err = pytest.New(test.Name, raw)
	default:
		return nil, fmt.Errorf("unsupported test framework %#q", test.Definition.Type)
	}

	if err != nil {
		return nil, fmt.Errorf("configure test framework:\n%w", err)
	}

	return runner, nil
}

type testInputs struct {
	Image    *artifacts.Artifact  `json:"image,omitempty"`
	Packages []artifacts.Artifact `json:"packages,omitempty"`
}

func resolveInputs(subject Subject, plans []plannedTest, options Options,
	resolver *artifacts.Resolver,
) ([]testInputs, error) {
	result := make([]testInputs, len(plans))

	if subject.Kind == imageSubject {
		formats := []string{"qcow2", "raw", "vhd", "vhdx", "oci"}
		for _, plan := range plans {
			formats = slices.DeleteFunc(formats, func(format string) bool {
				return !slices.Contains(plan.requirements.ImageFormats, format)
			})
		}

		if len(formats) == 0 {
			return nil, errors.New("selected tests require incompatible image formats; select compatible tests separately")
		}

		image, err := resolver.Image(
			subject.Name, options.Arch, formats, options.subjectSelection(subject.Kind), subject.Distro,
		)
		if err != nil {
			return nil, fmt.Errorf("resolve inputs:\n%w", err)
		}

		for index := range result {
			result[index].Image = &image
		}

		return result, nil
	}

	packages, err := resolver.Packages(subject.Name, options.Arch, options.subjectSelection(subject.Kind), subject.Distro)
	if err != nil {
		return nil, fmt.Errorf("resolve inputs:\n%w", err)
	}

	bases := make(map[string]artifacts.Artifact)

	for index, plan := range plans {
		result[index].Packages = packages

		if !plan.requirements.ImageRequired {
			continue
		}

		key := plan.baseName + ":" + strings.Join(plan.requirements.ImageFormats, ",")

		image, exists := bases[key]
		if !exists {
			image, err = resolver.Image(
				plan.baseName, options.Arch, plan.requirements.ImageFormats, options.baseSelection(), subject.Distro,
			)
			if err != nil {
				return nil, fmt.Errorf("resolve inputs:\n%w", err)
			}

			bases[key] = image
		}

		result[index].Image = &image
	}

	return result, nil
}

func executeOne(env *azldev.Env, subject Subject, plan plannedTest, inputs testInputs, options Options,
	runDir string, failed bool,
) (testframework.RunResult, error) {
	result := testframework.RunResult{
		Name: plan.test.Name, Framework: plan.test.Definition.Type,
		Strategy: plan.execution.Strategy, OutputDir: filepath.Join(runDir, plan.test.Name),
	}
	if err := env.Err(); err != nil {
		result.Status, result.Error = testframework.Cancelled, err.Error()

		return result, fmt.Errorf("test cancelled:\n%w", err)
	}

	if failed && options.FailFast {
		result.Status, result.Error = testframework.Skipped, "not started after earlier failure"

		return result, nil
	}

	if env.DryRun() {
		result.Status = testframework.Planned
		slog.Info("Would run test", "test", result.Name, "framework", result.Framework, "strategy", result.Strategy)

		return result, nil
	}

	request, err := prepareRequest(env, subject, plan, inputs, result.OutputDir)
	if err != nil {
		result.Status, result.Error = testframework.Errored, err.Error()

		return result, err
	}

	start := time.Now()
	native, err := plan.runner.Run(env, request)
	result.Duration = time.Since(start)

	result.Status, result.JUnitPath = native.Status, native.JUnitPath

	result.Cases = native.Cases
	if err != nil {
		result.Status, result.Error = testframework.Errored, err.Error()
		if errors.Is(err, testframework.ErrTestFailed) {
			result.Status = testframework.Failed
		}

		if env.Err() != nil {
			result.Status = testframework.Cancelled
		}
	} else if result.Status == "" {
		result.Status = testframework.Passed
	}

	if err != nil {
		return result, fmt.Errorf("execute test:\n%w", err)
	}

	return result, nil
}

func prepareRequest(env *azldev.Env, subject Subject, plan plannedTest, inputs testInputs,
	workDir string,
) (testframework.RunRequest, error) {
	request := testframework.RunRequest{
		Execution: plan.execution, SubjectName: subject.Name,
		Capabilities: subject.Capabilities, Properties: subject.Properties, WorkDir: workDir,
		CacheDir: filepath.Join(env.WorkDir(), "test-cache"), JUnitPath: filepath.Join(workDir, "junit.xml"),
		InputsPath: filepath.Join(workDir, "inputs.json"),
		Image:      inputs.Image, Packages: inputs.Packages,
	}

	if err := fileutils.MkdirAll(env.FS(), workDir); err != nil {
		return request, fmt.Errorf("prepare request:\n%w", err)
	}

	data, err := json.MarshalIndent(struct {
		Subject   string                      `json:"subject"`
		Kind      string                      `json:"kind"`
		Inputs    testInputs                  `json:"inputs"`
		Test      projectconfig.ResolvedTest  `json:"test"`
		Execution testframework.ExecutionSpec `json:"execution"`
	}{subject.Name, subject.Kind, inputs, plan.test, plan.execution}, "", "  ")
	if err != nil {
		return request, fmt.Errorf("prepare request:\n%w", err)
	}

	if err := fileutils.WriteFile(env.FS(), request.InputsPath, data, fileperms.PrivateFile); err != nil {
		return request, fmt.Errorf("prepare request:\n%w", err)
	}

	return request, nil
}
