// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lisa_test

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/lisa"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lisaExitError int

func (e lisaExitError) Error() string { return fmt.Sprintf("native exit %d", e) }
func (e lisaExitError) ExitCode() int { return int(e) }

func TestNativeFailedCountMustMatchReportedFailures(t *testing.T) {
	for _, item := range []struct {
		failures, code int
		want           testframework.Status
	}{
		{1, 1, testframework.Failed},
		{2, 2, testframework.Failed},
		{1, 2, testframework.Errored},
		{1, 0, testframework.Errored},
		{0, 1, testframework.Errored},
	} {
		ctx := setup(t)
		ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
			if filepath.Base(cmd.Args[0]) != lisaExecutable {
				return nil
			}

			cases := `<testcase name="pass"/>`
			if item.failures > 0 {
				cases = strings.Repeat(`<testcase name="fail"><failure/></testcase>`, item.failures)
			}

			write(t, ctx, request().JUnitPath, "<testsuite>"+cases+"</testsuite>")

			if item.code != 0 {
				return lisaExitError(item.code)
			}

			return nil
		}
		runner, err := lisa.New("smoke", map[string]any{"name": "check"})
		require.NoError(t, err)
		result, err := runner.Run(ctx, request())
		require.Error(t, err)
		assert.Equal(t, item.want, result.Status)
		require.NoError(t, result.Validate(err))
		require.NotEmpty(t, result.Cases)
	}
}

func TestOptionalPythonOptionsDoNotCreateEmptyInstallRequests(t *testing.T) {
	ctx := setup(t)
	installs := 0
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if contains(cmd.Args, "pip") {
			installs++

			assert.Equal(t, []string{"-m", "pip", "install", "--quiet", "-e", cmd.Dir}, cmd.Args[1:])
		}

		if filepath.Base(cmd.Args[0]) == lisaExecutable {
			runbook, err := fileutils.ReadFile(ctx.FS(), cmd.Args[2])
			require.NoError(t, err)
			assert.NotContains(t, string(runbook), "transformer:", "image-only runs must not install candidates")
			write(t, ctx, request().JUnitPath, `<testsuite><testcase name="pass"/></testsuite>`)
		}

		return nil
	}
	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)
	_, err = runner.Run(ctx, request())
	require.NoError(t, err)
	assert.Equal(t, 1, installs)
}

func TestLISARejectsUnconsumedOverrides(t *testing.T) {
	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)

	for _, spec := range []testframework.ExecutionSpec{
		{Strategy: testframework.VM, FrameworkDir: "/lisa", SourceDir: "/tests"},
		{Strategy: testframework.VM, FrameworkDir: "/lisa", Plan: "/plan"},
	} {
		_, err := runner.Requirements(spec)
		require.ErrorContains(t, err, "does not consume")
	}

	req := request()
	req.Packages = []artifact.Artifact{{Path: "/candidate.txt", Format: "rpm"}}
	ctx := setup(t)
	ctx.DryRunValue = true
	_, err = runner.Run(ctx, req)
	require.ErrorContains(t, err, "absolute RPM path")
}

func TestDeploymentFailureIsNotATestAssertion(t *testing.T) {
	ctx := setup(t)
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if filepath.Base(cmd.Args[0]) != lisaExecutable {
			return nil
		}

		write(t, ctx, request().JUnitPath, `<testsuite tests="1" failures="1">`+
			`<testcase name="case"><failure message="deployment failed. RuntimeError: candidate installation failed"/>`+
			`</testcase></testsuite>`)

		return lisaExitError(1)
	}
	runner, err := lisa.New("smoke", map[string]any{"name": "check"})
	require.NoError(t, err)
	result, err := runner.Run(ctx, request())
	require.Error(t, err)
	assert.Equal(t, testframework.Errored, result.Status)
	require.Len(t, result.Cases, 1)
	assert.Equal(t, testframework.Errored, result.Cases[0].Status)
	assert.Equal(t, "failure", result.Cases[0].NativeStatus)
	assert.Contains(t, result.Cases[0].Message, "candidate installation failed")

	data, readErr := fileutils.ReadFile(ctx.FS(), request().JUnitPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "<failure", "retain the native report unchanged")
}
