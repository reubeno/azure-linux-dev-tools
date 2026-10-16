// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testsource_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testsource"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/cachekey"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDestinationRelationshipValidation(t *testing.T) {
	for _, pair := range [][2]string{
		{"/source", "/source"},
		{"/source", "/source/nested"},
		{"/source/nested", "/source"},
		{"/source", "/"},
		{"relative", "/dest"},
		{"/source", "relative"},
	} {
		require.Error(t, testsource.CheckDestination(pair[0], pair[1]))
	}

	require.NoError(t, testsource.CheckDestination("/source", "/source-other"))
	require.NoError(t, testsource.CheckDestination("", "/destination"))
}

func TestLocalSourcesAreNeverCheckedOut(t *testing.T) {
	env := testutils.NewTestEnv(t)
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/checkout"))
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/checkout/test.py", []byte("original"), fileperms.PrivateFile))
	dir, err := testsource.Materialize(env.Env, nil, "/checkout", "/run/source")
	require.NoError(t, err)
	assert.Equal(t, "/run/source", dir)
	require.NoError(t, fileutils.WriteFile(env.TestFS, dir+"/test.py", []byte("modified"), fileperms.PrivateFile))
	data, err := fileutils.ReadFile(env.TestFS, "/checkout/test.py")
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
	assert.Empty(t, env.CommandsExecuted)
	_, err = testsource.Materialize(env.Env, nil, "/missing", "/run/missing")
	require.Error(t, err)
}

func TestManagedSourcesNeverReuseDirtyCheckouts(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("git")
	env.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		env.CommandsExecuted = append(env.CommandsExecuted, cmd.Args)

		return nil
	}
	source := projectconfig.GitSourceConfig{GitURL: "https://example.test/a.git", Ref: strings.Repeat("a", 40)}
	legacy := filepath.Join("/cache", "sources", cachekey.From(source.GitURL, source.Ref))
	require.NoError(t, fileutils.MkdirAll(env.TestFS, legacy))
	require.NoError(t, fileutils.WriteFile(env.TestFS, legacy+"/dirty.py", nil, fileperms.PrivateFile))
	first, err := testsource.Materialize(env.Env, &source, "", "/run/first")
	require.NoError(t, err)

	require.NoError(t, fileutils.WriteFile(env.TestFS, first+"/dirty.py", nil, fileperms.PrivateFile))
	second, err := testsource.Materialize(env.Env, &source, "", "/run/second")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	require.Len(t, env.CommandsExecuted, 4)
	assert.Contains(t, env.CommandsExecuted[1], source.Ref)
	assert.Equal(t, []string{"git", "clone", "--no-hardlinks", source.GitURL, second}, env.CommandsExecuted[2])
	exists, err := fileutils.Exists(env.TestFS, second+"/dirty.py")
	require.NoError(t, err)
	assert.False(t, exists)
	exists, err = fileutils.Exists(env.TestFS, legacy+"/dirty.py")
	require.NoError(t, err)
	assert.True(t, exists, "unowned legacy cache must not be cleaned")
}

func TestExistingDestinationAndOverlapsAreNeverModified(t *testing.T) {
	for _, destination := range []string{"/checkout", "/checkout/nested", "/run/existing", "/"} {
		t.Run(destination, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			for _, dir := range []string{"/checkout", "/run/existing"} {
				require.NoError(t, fileutils.MkdirAll(env.TestFS, dir))
				require.NoError(t, fileutils.WriteFile(env.TestFS, dir+"/keep", []byte("keep"), fileperms.PrivateFile))
			}

			_, err := testsource.Materialize(env.Env, nil, "/checkout", destination)
			require.Error(t, err)

			for _, dir := range []string{"/checkout", "/run/existing"} {
				data, err := fileutils.ReadFile(env.TestFS, dir+"/keep")
				require.NoError(t, err)
				assert.Equal(t, "keep", string(data))
			}
		})
	}
}

func TestFailedAndCancelledMaterializationCleansOnlyOwnedSource(t *testing.T) {
	for _, stage := range []string{"clone", "checkout", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			env := testutils.NewTestEnv(t)

			ctx, cancel := env.Env.WithCancel()
			defer cancel()

			env.CmdFactory.RegisterCommandInSearchPath("git")

			source := projectconfig.GitSourceConfig{GitURL: "https://example.test/a.git", Ref: strings.Repeat("a", 40)}
			env.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
				require.NoError(t, fileutils.WriteFile(env.TestFS, "/run/source/partial", nil, fileperms.PrivateFile))

				if stage == "cancelled" {
					cancel()

					return nil
				}

				if cmd.Args[1] == stage || (stage == "checkout" && cmd.Args[1] == "-C") {
					return errors.New("injected failure")
				}

				return nil
			}
			_, err := testsource.Materialize(ctx, &source, "", "/run/source")
			require.Error(t, err)

			if stage == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}

			exists, err := fileutils.Exists(env.TestFS, "/run/source")
			require.NoError(t, err)
			assert.False(t, exists)

			env.CmdFactory.RunHandler = nil
			_, err = testsource.Materialize(env.Env, &source, "", "/run/source")
			require.NoError(t, err, "a partial checkout must not poison the next attempt")
		})
	}
}

func TestLinkedWorktreeDoesNotRetainCallerGitPointer(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("git")
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/checkout"))
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/original/.git/worktrees/checkout"))

	pointer := "gitdir: /original/.git/worktrees/checkout\n"
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/checkout/.git", []byte(pointer), fileperms.PrivateFile))
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/checkout/dirty.py", []byte("uncommitted"), fileperms.PrivateFile))

	var commands [][]string

	env.CmdFactory.RunHandler = func(command *exec.Cmd) error {
		commands = append(commands, command.Args)
		if command.Args[1] == "clone" {
			require.NoError(t, fileutils.MkdirAll(env.TestFS, "/run/source/.git"))
			require.NoError(t, fileutils.WriteFile(env.TestFS, "/run/source/.git/index", []byte("private index"),
				fileperms.PrivateFile))
		}

		return nil
	}

	dir, err := testsource.Materialize(env.Env, nil, "/checkout", "/run/source")
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"git", "clone", "--no-hardlinks", "--no-checkout", "/checkout", "/run/source"},
		{"git", "-C", "/run/source", "reset", "--mixed", "HEAD"},
	}, commands)

	info, err := env.TestFS.Stat(dir + "/.git")
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	data, err := fileutils.ReadFile(env.TestFS, dir+"/dirty.py")
	require.NoError(t, err)
	assert.Equal(t, "uncommitted", string(data))
	require.NoError(t, fileutils.WriteFile(env.TestFS, dir+"/.git/index", nil, fileperms.PrivateFile))
	require.NoError(t, env.TestFS.RemoveAll(dir))
	data, err = fileutils.ReadFile(env.TestFS, "/checkout/.git")
	require.NoError(t, err)
	assert.Equal(t, pointer, string(data))

	exists, err := fileutils.DirExists(env.TestFS, "/original/.git/worktrees/checkout")
	require.NoError(t, err)
	assert.True(t, exists)
}
