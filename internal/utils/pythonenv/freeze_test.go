// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pythonenv_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreezeDoesNotRequirePipOrEnableBytecodeWrites(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
		assert.Equal(t, []string{"/prepared/bin/python3", "-I", "-B", "-c"}, cmd.Args[:4])
		assert.Contains(t, cmd.Args[4], "importlib.metadata")
		assert.Contains(t, cmd.Args[4], "direct_url.json")
		assert.Contains(t, cmd.Args[4], "# source ")
		assert.NotContains(t, cmd.Args, "pip")

		return "# Python fixture\npytest==9.0.2", nil
	}
	require.NoError(t, pythonenv.Freeze(ctx, "/prepared/bin/python3", "/reports/python-environment.txt"))
	data, err := fileutils.ReadFile(ctx.FS(), "/reports/python-environment.txt")
	require.NoError(t, err)
	assert.Contains(t, string(data), "pytest==9.0.2")
}

func TestFreezeCancellationAndNativeErrors(t *testing.T) {
	ctx := testctx.NewCtx()
	lifetime, cancel := context.WithCancel(t.Context())
	cancel()

	ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
		t.Fatal("a cancelled query must not execute")

		return "", nil
	}
	require.ErrorIs(t, pythonenv.Freeze(opctx.WithContext(ctx, lifetime), "/python", "/report"), context.Canceled)

	nativeErr := errors.New("bad metadata")
	ctx.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
		_, err := cmd.Stderr.Write([]byte("metadata diagnostic"))
		require.NoError(t, err)

		return "", nativeErr
	}
	err := pythonenv.Freeze(ctx, "/python", "/report")
	require.ErrorIs(t, err, nativeErr)
	require.ErrorContains(t, err, "metadata diagnostic")
	exists, err := fileutils.Exists(ctx.FS(), "/report")
	require.NoError(t, err)
	assert.False(t, exists)
}
