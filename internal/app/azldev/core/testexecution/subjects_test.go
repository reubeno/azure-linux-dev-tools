// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindComponentWithoutPackagingLocks(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.Config.Components["one"] = projectconfig.ComponentConfig{
		Name: "one", Spec: projectconfig.SpecSource{SourceType: projectconfig.SpecSourceTypeUpstream},
	}
	env.Config.Components["other"] = projectconfig.ComponentConfig{Name: "other"}

	set, err := testexecution.FindComponent(env.Env, "one")
	require.NoError(t, err)
	assert.Equal(t, []string{"one"}, set.Names())

	_, err = testexecution.FindComponent(env.Env, "missing")
	require.ErrorContains(t, err, "component not found")
	_, err = testexecution.FindComponent(env.Env, "*")
	require.ErrorContains(t, err, "exactly one")
	assert.Empty(t, env.CommandsExecuted)
}

func TestFindImageAcceptsLocalAndPublishedOnlyImages(t *testing.T) {
	env := testutils.NewTestEnv(t)
	ref := env.Config.Project.DefaultDistro
	distro := env.Config.Distros[ref.Name]
	version := distro.Versions[ref.Version]
	version.Images = map[string]projectconfig.PublishedImage{"published": {}}
	distro.Versions[ref.Version] = version
	env.Config.Distros[ref.Name] = distro

	published, err := testexecution.FindImage(env.Env, "published")
	require.NoError(t, err)
	assert.Equal(t, "published", published.Name)
	assert.Empty(t, env.Config.Images)
	_, err = testexecution.FindImage(env.Env, "missing")
	require.ErrorContains(t, err, "not defined locally")

	local := projectconfig.ImageConfig{Name: "local", Description: "Project image"}
	env.Config.Images["local"] = local
	found, err := testexecution.FindImage(env.Env, "local")
	require.NoError(t, err)
	assert.Equal(t, local, found)

	env.Config.Project.DefaultDistro.Name = "missing"
	_, err = testexecution.FindImage(env.Env, "published")
	require.ErrorContains(t, err, "find test image")
	assert.Empty(t, env.CommandsExecuted)
}
