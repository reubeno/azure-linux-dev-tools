// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedRunner struct{}

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
