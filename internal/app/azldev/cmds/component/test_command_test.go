// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package component_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/component"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComponentTestCommandFlags(t *testing.T) {
	cmd := component.NewComponentTestCmd()
	assert.Equal(t, "test COMPONENT", cmd.Use)
	require.NotNil(t, cmd.RunE)

	for _, flag := range []string{
		"test", "execution", "artifact-source", "rebuild", "rpm", "base-image",
		"base-image-path", "base-image-source", "rebuild-base-image", "ssh-host", "ssh-user", "junit-xml",
	} {
		require.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}

	assert.Empty(t, cmd.Flags().Lookup("rpm").Annotations["cobra_annotation_bash_completion_one_required_flag"])
}

func TestComponentTestNoMatch(t *testing.T) {
	env := testutils.NewTestEnv(t)
	cmd := component.NewComponentTestCmd()
	cmd.SetArgs([]string{"does-not-exist"})
	require.ErrorContains(t, cmd.ExecuteContext(env.Env), "component not found")
	assert.Empty(t, env.CommandsExecuted)
}
