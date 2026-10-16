// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pytest_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/pytest"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPinnedSourceWithoutOverrideUsesCheckoutRelativeWorkingDirectory(t *testing.T) {
	ctx := newContext(t)
	ctx.CmdFactory.RegisterCommandInSearchPath("git")
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")

	runner, err := pytest.New("smoke", map[string]any{
		"source":      map[string]any{"git-url": "https://example.test/tests", "ref": strings.Repeat("a", 40)},
		"working-dir": "suite", "install": "requirements", "test-paths": []string{"test.py"},
	})
	require.NoError(t, err)

	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if cmd.Args[0] == "git" && cmd.Args[1] == "clone" {
			assert.Contains(t, cmd.Args, "--no-hardlinks")
			directory := cmd.Args[len(cmd.Args)-1]
			write(t, ctx, directory+"/suite/requirements.txt", "pytest")
			write(t, ctx, directory+"/suite/test.py", "source")
		}

		if len(cmd.Args) > 2 && cmd.Args[2] == pytestModule {
			assert.True(t, strings.HasSuffix(cmd.Dir, "/source/suite"))
			write(t, ctx, request().JUnitPath, passedJUnit)
		}

		return nil
	}
	result, err := runner.Run(ctx, request())
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
}

func TestLocalOverrideReplacesAbsoluteConfiguredWorkingDirectory(t *testing.T) {
	ctx := newContext(t)
	ctx.CmdFactory.RegisterCommandInSearchPath("python3")
	write(t, ctx, "/override/requirements.txt", "pytest")
	write(t, ctx, "/outside/test_external.py", "caller-owned explicit selector")

	runner, err := pytest.New("smoke", map[string]any{
		"working-dir": "/original", "install": "requirements", "test-paths": []string{"/outside/test_external.py"},
	})
	require.NoError(t, err)

	req := request()
	req.Execution.SourceDir = "/override"
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if len(cmd.Args) > 2 && cmd.Args[2] == pytestModule {
			assert.Equal(t, "source", filepath.Base(cmd.Dir))
			assert.Equal(t, "/outside/test_external.py", filepath.Clean(filepath.Join(cmd.Dir, cmd.Args[3])))
			write(t, ctx, req.JUnitPath, passedJUnit)
		}

		return nil
	}
	_, err = runner.Run(ctx, req)
	require.NoError(t, err)
}

func TestPytestRejectsUnconsumedOverridesAndRelativeManifest(t *testing.T) {
	runner, err := pytest.New("smoke", nil)
	require.NoError(t, err)

	for _, spec := range []testframework.ExecutionSpec{
		{Strategy: testframework.Host, FrameworkDir: "/lisa"},
		{Strategy: testframework.Host, Plan: "/plan"},
	} {
		_, err := runner.Requirements(spec)
		require.ErrorContains(t, err, "does not consume")
	}

	ctx := newContext(t)
	ctx.DryRunValue = true
	req := request()
	req.InputsPath = "relative.json"
	_, err = runner.Run(ctx, req)
	require.ErrorContains(t, err, "manifest path must be absolute")
}
