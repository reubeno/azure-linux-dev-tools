// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testsource_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testsource"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalSourcesAreNeverCheckedOut(t *testing.T) {
	env := testutils.NewTestEnv(t)
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/checkout"))
	dir, err := testsource.Materialize(env.Env, nil, "/checkout", "/cache")
	require.NoError(t, err)
	assert.Equal(t, "/checkout", dir)
	assert.Empty(t, env.CommandsExecuted)
	_, err = testsource.Materialize(env.Env, nil, "/missing", "/cache")
	require.Error(t, err)
}

func TestManagedSourceIdentityIncludesRepository(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("git")
	env.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		env.CommandsExecuted = append(env.CommandsExecuted, cmd.Args)

		return nil
	}
	source := projectconfig.GitSourceConfig{GitURL: "https://example.test/a.git", Ref: strings.Repeat("a", 40)}
	first, err := testsource.Materialize(env.Env, &source, "", "/cache")
	require.NoError(t, err)
	source.GitURL = "https://example.test/b.git"
	second, err := testsource.Materialize(env.Env, &source, "", "/cache")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	require.Len(t, env.CommandsExecuted, 4)
	assert.Contains(t, env.CommandsExecuted[1], source.Ref)
}
