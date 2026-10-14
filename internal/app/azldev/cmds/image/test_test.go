// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package image_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/image"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewImageTestCmd(t *testing.T) {
	cmd := image.NewImageTestCmd()
	require.NotNil(t, cmd)
	assert.Equal(t, "test IMAGE_NAME", cmd.Use)
	assert.Contains(t, cmd.Short, "test")
}

func TestNewImageTestCmd_Flags(t *testing.T) {
	cmd := image.NewImageTestCmd()

	assert.NotNil(t, cmd.Flags().Lookup("test-suite"))
	assert.NotNil(t, cmd.Flags().Lookup("image-path"))
	assert.NotNil(t, cmd.Flags().Lookup("junit-xml"))
}

func TestPublishedImageDoesNotRequireLocalRecipe(t *testing.T) {
	testEnv := testutils.NewTestEnv(t)
	distroName := testEnv.Config.Project.DefaultDistro.Name
	versionName := testEnv.Config.Project.DefaultDistro.Version
	distro := testEnv.Config.Distros[distroName]
	version := distro.Versions[versionName]
	version.Images = map[string]projectconfig.PublishedImage{
		"published": {Architectures: map[string][]projectconfig.PublishedImageArtifact{
			"x86_64": {{Format: "qcow2", URI: "https://example.test/base.qcow2"}},
		}},
	}
	distro.Versions[versionName] = version
	testEnv.Config.Distros[distroName] = distro
	testEnv.Config.Tests["smoke"] = projectconfig.TestDefinition{
		Type: "pytest", Pytest: map[string]any{
			"working-dir": "/project/tests", "test-paths": []string{"test_image.py"},
		},
	}
	options := azldev.NewEnvOptions()
	options.Config = testEnv.Config
	options.ProjectDir = testEnv.Env.ProjectDir()
	options.Interfaces = testEnv.TestInterfaces
	options.EventListener = testEnv.EventListener
	options.DryRunnable = azldev.NewAppDryRunnable(true)
	env := azldev.NewEnv(t.Context(), options)
	cmd := image.NewImageTestCmd()
	cmd.SetArgs([]string{"published", "--artifact-source", "lkg", "--test", "smoke", "--arch", "x86_64"})
	require.NoError(t, cmd.ExecuteContext(env))
	assert.Empty(t, testEnv.Config.Images)
	assert.Empty(t, testEnv.CommandsExecuted, "a dry-run must not build, download, or invoke a framework")
}
