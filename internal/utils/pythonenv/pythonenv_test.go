// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package pythonenv_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/pythonenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRejectsExistingEnvironments(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("python3")
	python, err := pythonenv.Create(env.Env, "/run/venv")
	require.NoError(t, err)
	assert.Equal(t, "/run/venv/bin/python3", python)
	require.Len(t, env.CommandsExecuted, 1)
	assert.Equal(t, []string{"python3", "-m", "venv", "/run/venv"}, env.CommandsExecuted[0])
	require.NoError(t, fileutils.MkdirAll(env.TestFS, filepath.Dir(python)))
	require.NoError(t, fileutils.WriteFile(env.TestFS, python, nil, fileperms.PrivateFile))
	_, err = pythonenv.Create(env.Env, "/run/venv")
	require.Error(t, err)
	assert.Len(t, env.CommandsExecuted, 1)
	exists, err := fileutils.Exists(env.TestFS, python)
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFailedOrCancelledCreationCannotPoisonNextAttempt(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancelled], func(t *testing.T) {
			env := testutils.NewTestEnv(t)

			ctx, cancel := env.Env.WithCancel()
			defer cancel()

			env.CmdFactory.RegisterCommandInSearchPath("python3")
			env.CmdFactory.RunHandler = func(_ *exec.Cmd) error {
				require.NoError(t, fileutils.MkdirAll(env.TestFS, "/run/venv/bin"))
				require.NoError(t, fileutils.WriteFile(env.TestFS, "/run/venv/bin/python3", nil, fileperms.PrivateFile))

				if cancelled {
					cancel()

					return nil
				}

				return errors.New("partial venv")
			}
			_, err := pythonenv.Create(ctx, "/run/venv")
			require.Error(t, err)

			if cancelled {
				require.ErrorIs(t, err, context.Canceled)
			}

			exists, err := fileutils.Exists(env.TestFS, "/run/venv")
			require.NoError(t, err)
			assert.False(t, exists)

			env.CmdFactory.RunHandler = nil
			_, err = pythonenv.Create(env.Env, "/run/venv")
			require.NoError(t, err)
		})
	}
}

func TestExistingRequiresCompleteCallerEnvironmentWithoutCommands(t *testing.T) {
	env := testutils.NewTestEnv(t)
	_, err := pythonenv.Existing(env.Env, "/caller/venv")
	require.ErrorContains(t, err, "missing or incomplete")
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/caller/venv/bin"))
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/caller/venv/bin/python3", nil, fileperms.PrivateFile))
	_, err = pythonenv.Existing(env.Env, "/caller/venv")
	require.ErrorContains(t, err, "missing or incomplete")
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/caller/venv/pyvenv.cfg", nil, fileperms.PrivateFile))
	python, err := pythonenv.Existing(env.Env, "/caller/venv")
	require.NoError(t, err)
	assert.Equal(t, "/caller/venv/bin/python3", python)
	assert.Empty(t, env.CommandsExecuted)
}

func TestInstallPropagatesFailuresAndCancellation(t *testing.T) {
	env := testutils.NewTestEnv(t)
	nativeErr := errors.New("installation failed")
	env.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		assert.Equal(t, []string{"/python", "-m", "pip", "install", "--quiet", "pytest"}, cmd.Args)
		assert.Equal(t, "/source", cmd.Dir)

		return nativeErr
	}
	require.ErrorIs(t, pythonenv.Install(env.Env, "/python", "/source", "pytest"), nativeErr)
	cancelled, cancel := env.Env.WithCancel()
	cancel()

	env.CmdFactory.RunHandler = func(*exec.Cmd) error {
		t.Fatal("cancelled installation executed")

		return nil
	}

	require.ErrorIs(t, pythonenv.Install(cancelled, "/python", "/source", "pytest"), context.Canceled)
}
