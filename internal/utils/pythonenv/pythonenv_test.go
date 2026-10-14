// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pythonenv_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureCreatesAndReusesEnvironment(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("python3")
	python, err := pythonenv.Ensure(env.Env, "/cache/venv")
	require.NoError(t, err)
	assert.Equal(t, "/cache/venv/bin/python3", python)
	require.Len(t, env.CommandsExecuted, 1)
	assert.Equal(t, []string{"python3", "-m", "venv", "/cache/venv"}, env.CommandsExecuted[0])
	require.NoError(t, fileutils.WriteFile(env.TestFS, python, nil, fileperms.PrivateFile))
	_, err = pythonenv.Ensure(env.Env, "/cache/venv")
	require.NoError(t, err)
	assert.Len(t, env.CommandsExecuted, 1)
}

func TestCacheKeysIncludeAllInputs(t *testing.T) {
	assert.NotEqual(t, pythonenv.Key("ab", "c"), pythonenv.Key("a", "bc"))
	assert.NotEqual(t, pythonenv.Key("tool", "v1"), pythonenv.Key("tool", "v2"))
}
