// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pytest_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/artifact"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/pytest"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPreparedContext(t *testing.T) *testctx.TestCtx {
	t.Helper()

	ctx := newContext(t)
	require.NoError(t, ctx.OSEnv().Chdir("/tests"))
	write(t, ctx, "/tests/test.py", "caller source")
	write(t, ctx, "/project-work/pytest-venv/smoke/bin/python3", "caller Python")
	write(t, ctx, "/project-work/pytest-venv/smoke/pyvenv.cfg", "home = /python")

	return ctx
}

func TestOCIIdentityCannotMasqueradeAsAnImagePath(t *testing.T) {
	ctx := newPreparedContext(t)
	ctx.DryRunValue = true
	runner, err := pytest.New("smoke", map[string]any{"extra-args": []string{"--image={image-path}"}})
	require.NoError(t, err)

	req := request()
	req.Image = &artifact.Artifact{OCIImageID: "sha256:" + strings.Repeat("a", 64), Format: "oci"}
	_, err = runner.Run(ctx, req)
	require.ErrorContains(t, err, "requires a file artifact")
}

func TestMainPreparedEnvironmentRemainsUsable(t *testing.T) {
	ctx := newPreparedContext(t)
	runner, err := pytest.New("smoke", map[string]any{"working-dir": "/tests", "test-paths": []string{"test.py"}})
	require.NoError(t, err)

	req := request()
	req.PreparedPythonDir = "/project-work/pytest-venv/smoke"
	ctx.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		assert.Equal(t, req.PreparedPythonDir+"/bin/python3", cmd.Args[0])
		assert.NotContains(t, cmd.Args, "pip")
		assert.NotContains(t, cmd.Args, "venv")

		if len(cmd.Args) > 2 && cmd.Args[2] == pytestModule {
			write(t, ctx, req.JUnitPath,
				`<testsuite tests="1"><testcase classname="suite" name="test" time="0.25"/></testsuite>`)
		}

		return nil
	}
	before, err := fileutils.Glob(ctx.FS(), req.PreparedPythonDir+"/**/*")
	require.NoError(t, err)
	result, err := runner.Run(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, testframework.Passed, result.Status)
	require.Len(t, result.Cases, 1)

	after, err := fileutils.Glob(ctx.FS(), req.PreparedPythonDir+"/**/*")
	require.NoError(t, err)
	assert.Equal(t, before, after)

	data, err := fileutils.ReadFile(ctx.FS(), filepath.Join(req.PreparedPythonDir, "bin/python3"))
	require.NoError(t, err)
	assert.Equal(t, "caller Python", string(data))
}
