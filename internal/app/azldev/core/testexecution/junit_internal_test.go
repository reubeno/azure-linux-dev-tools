// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedRunner struct{}

func TestRequestPreservesArtifactMetadata(t *testing.T) {
	env := testutils.NewTestEnv(t)
	image := &artifact.Artifact{
		Path: "/images/base.qcow2", Format: "qcow2", Arch: "aarch64",
		Source: "lkg", SHA256: "image-digest", URI: "https://example.test/base",
	}
	packages := []artifact.Artifact{{Path: "/rpms/pkg.rpm", Format: "rpm", Arch: "aarch64", Source: "local"}}
	request, err := prepareRequest(env.Env, Subject{Name: "pkg"}, plannedTest{},
		testInputs{Image: image, Packages: packages}, "/work/run")
	require.NoError(t, err)
	assert.Equal(t, *image, *request.Image)
	assert.Equal(t, packages, request.Packages)
	assert.Equal(t, "/images/base.qcow2", request.ImagePath())
	assert.Equal(t, []string{"/rpms/pkg.rpm"}, request.RPMPaths())
}

func TestBaseOverrideOnlyAppliesToImageConsumers(t *testing.T) {
	tests := []projectconfig.ResolvedTest{
		{Name: "tmt", Definition: projectconfig.TestDefinition{Type: "tmt", Tmt: map[string]any{}}},
		{Name: "pytest", Definition: projectconfig.TestDefinition{Type: "pytest", Pytest: map[string]any{
			"working-dir": "/tests", "test-paths": []string{"test_rpm.py"},
		}}},
	}
	options := Options{SourceDir: "/tests", Plan: "/plans/all", BaseImagePath: "/base.qcow2"}
	plans, err := planTests(Subject{Name: "curl", Kind: componentSubject}, tests, options)
	require.NoError(t, err)
	require.Len(t, plans, 2)
	assert.Equal(t, "explicit-base", plans[0].baseName)
	assert.Empty(t, plans[1].baseName)

	options.Plan = ""
	_, err = planTests(Subject{Name: "curl", Kind: componentSubject}, tests[1:], options)
	require.ErrorContains(t, err, "base image options")
}

func TestMixedFrameworkOverridesAreRoutedPerTest(t *testing.T) {
	tests := []projectconfig.ResolvedTest{
		{Name: "lisa", Definition: projectconfig.TestDefinition{Type: "lisa", Lisa: map[string]any{"name": "smoke"}}},
		{Name: "tmt", Definition: projectconfig.TestDefinition{Type: "tmt", Tmt: map[string]any{}}},
	}
	plans, err := planTests(Subject{Name: "curl", Kind: componentSubject}, tests, Options{
		LisaDir: "/lisa", SourceDir: "/tests", Plan: "/plans/all", BaseImagePath: "/base.qcow2",
	})
	require.NoError(t, err)
	assert.Equal(t, "/lisa", plans[0].execution.FrameworkDir)
	assert.Empty(t, plans[0].execution.SourceDir)
	assert.Empty(t, plans[0].execution.Plan)
	assert.Empty(t, plans[1].execution.FrameworkDir)
	assert.Equal(t, "/tests", plans[1].execution.SourceDir)
	assert.Equal(t, "/plans/all", plans[1].execution.Plan)
	_, err = planTests(Subject{Name: "curl", Kind: componentSubject}, tests[:1], Options{Plan: "/unused"})
	require.ErrorContains(t, err, "'--plan' requires")
}

func TestLegacyCapabilityMetadataIsPreserved(t *testing.T) {
	test := projectconfig.ResolvedTest{
		Name: "legacy",
		Definition: projectconfig.TestDefinition{
			Type: "pytest", RequiredCapabilities: []string{"custom-external-capability"},
			Pytest: map[string]any{"working-dir": "/tests", "test-paths": []string{"test_image.py"}},
		},
	}
	plans, err := planTests(Subject{Name: "base", Kind: imageSubject},
		[]projectconfig.ResolvedTest{test}, Options{})
	require.NoError(t, err)
	require.Len(t, plans, 1)
	assert.Equal(t, test.Definition.RequiredCapabilities, plans[0].test.Definition.RequiredCapabilities)
}

func (failedRunner) Requirements(testframework.ExecutionSpec) (testframework.Requirements, error) {
	return testframework.Requirements{}, nil
}

func (failedRunner) Run(opctx.Ctx, testframework.RunRequest) (testframework.RunResult, error) {
	return testframework.RunResult{}, testframework.ErrTestFailed
}

func TestExecutionFailuresAndFailFast(t *testing.T) {
	env := testutils.NewTestEnv(t)
	plan := plannedTest{runner: failedRunner{}}
	plan.test.Name = "failed"
	plan.test.Definition.Type = "test"
	result, err := executeOne(env.Env, Subject{Name: "subject"}, plan, testInputs{}, Options{}, "/work/run", false)
	require.ErrorIs(t, err, testframework.ErrTestFailed)
	assert.Equal(t, testframework.Failed, result.Status)
	result, err = executeOne(env.Env, Subject{}, plan, testInputs{}, Options{FailFast: true}, "/work/run", true)
	require.NoError(t, err)
	assert.Equal(t, testframework.Skipped, result.Status)

	cancelledEnv, cancel := env.Env.WithCancel()
	cancel()

	result, err = executeOne(cancelledEnv, Subject{}, plan, testInputs{}, Options{}, "/work/run", false)
	require.Error(t, err)
	assert.Equal(t, testframework.Cancelled, result.Status)
}

func TestJUnitInfrastructureErrorsAreRetained(t *testing.T) {
	env := testutils.NewTestEnv(t)
	native := "/work/native.xml"
	require.NoError(t, fileutils.WriteFile(env.TestFS, native,
		[]byte(`<testsuite name="native" tests="1"><testcase name="passed"/></testsuite>`), fileperms.PrivateFile))
	results := []testframework.RunResult{
		{Name: "one", Status: testframework.Errored, Error: "teardown failed", JUnitPath: native},
		{Name: "two", Status: testframework.Skipped, Error: "not started"},
	}
	require.NoError(t, writeJUnit(env.TestFS, "/work/results.xml", results))
	data, err := fileutils.ReadFile(env.TestFS, "/work/results.xml")
	require.NoError(t, err)
	assert.Contains(t, string(data), "teardown failed")
	assert.Contains(t, string(data), `<testcase name="passed"`)
	assert.Contains(t, string(data), "<skipped")

	_, err = readJUnit(env.TestFS, "/missing")
	require.Error(t, err)
	assert.NotErrorIs(t, err, testframework.ErrTestFailed)
}
