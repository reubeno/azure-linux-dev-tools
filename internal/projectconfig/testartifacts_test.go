// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublishedImageCatalogIsIndependentOfBuildRecipes(t *testing.T) {
	cfg := projectconfig.NewProjectConfig()
	err := toml.Unmarshal([]byte(`
[distros.azl.versions."4".test-base-images]
vm = "vm-base"
[distros.azl.versions."4".images.vm-base.architectures]
x86_64 = [{ format = "qcow2", uri = "https://example.test/lkg.qcow2" }]
aarch64 = [{ format = "qcow2", uri = "https://example.test/arm.qcow2" }]
[tests.smoke]
type = "tmt"
tmt = { plan = "/plans/all" }
execution = { strategy = "vm", base-images = { vm = "vm-base" } }
`), &cfg)
	require.NoError(t, err)
	require.NoError(t, cfg.Validate())
	assert.Empty(t, cfg.Images)
	assert.Equal(t, "vm-base", cfg.Distros["azl"].Versions["4"].TestBaseImages["vm"])
}

func TestPublishedCatalogRejectsInvalidArtifacts(t *testing.T) {
	for _, variant := range []projectconfig.PublishedImageArtifact{
		{Format: "bogus", URI: "https://example.test/a"},
		{Format: "qcow2", URI: "file:///tmp/a"},
		{Format: "qcow2", URI: "https://example.test/a", SHA256: "bad"},
		{Format: "qcow2", URI: "docker://registry.test/image"},
	} {
		cfg := projectconfig.NewProjectConfig()
		cfg.Distros["azl"] = projectconfig.DistroDefinition{Versions: map[string]projectconfig.DistroVersionDefinition{
			"4": {Images: map[string]projectconfig.PublishedImage{
				"base": {Architectures: map[string][]projectconfig.PublishedImageArtifact{"x86_64": {variant}}},
			}},
		}}
		require.Error(t, cfg.Validate())
	}
}

func TestExecutionDefaultsAreOptionalAndValidated(t *testing.T) {
	definition := projectconfig.TestDefinition{Type: "tmt", Tmt: map[string]any{}}
	require.NoError(t, definition.Validate("legacy"))
	definition.Execution.Strategy = "teleport"
	require.Error(t, definition.Validate("legacy"))
	definition.Execution = projectconfig.TestExecutionConfig{BaseImages: map[string]string{"host": "base"}}
	require.Error(t, definition.Validate("legacy"))
}

func TestImageTestDefaultsPreserveAbsentAndEmptyLists(t *testing.T) {
	cfg := projectconfig.NewProjectConfig()
	content := `
[distros.azl.versions."4".images.base.architectures]
x86_64 = [{ format = "qcow2", uri = "https://example.test/base.qcow2" }]
[distros.azl.versions."4".images.base.test-defaults.tmt]
cloud-init-runcmds = ["echo distro"]
[images.base.test-defaults.tmt]
cloud-init-runcmds = []
[images.other]
`
	require.NoError(t, toml.Unmarshal([]byte(content), &cfg))
	require.NoError(t, cfg.Validate())
	require.NotNil(t, cfg.Images["base"].TestDefaults.TMT.CloudInitRuncmds)
	assert.Empty(t, *cfg.Images["base"].TestDefaults.TMT.CloudInitRuncmds)
	assert.Nil(t, cfg.Images["other"].TestDefaults)
	assert.Equal(t, []string{"echo distro"},
		*cfg.Distros["azl"].Versions["4"].Images["base"].TestDefaults.TMT.CloudInitRuncmds)

	data, err := toml.Marshal(cfg)
	require.NoError(t, err)

	var roundtrip projectconfig.ProjectConfig
	require.NoError(t, toml.Unmarshal(data, &roundtrip))
	require.NotNil(t, roundtrip.Images["base"].TestDefaults.TMT.CloudInitRuncmds)
	assert.Empty(t, *roundtrip.Images["base"].TestDefaults.TMT.CloudInitRuncmds)
}

func TestImageTestDefaultsRejectMalformedCommands(t *testing.T) {
	for _, table := range []string{
		"images.base.test-defaults.tmt",
		`distros.azl.versions."4".images.base.test-defaults.tmt`,
	} {
		t.Run(table, func(t *testing.T) {
			cfg := projectconfig.NewProjectConfig()
			err := toml.Unmarshal([]byte("["+table+"]\ncloud-init-runcmds = [42]\n"), &cfg)
			require.Error(t, err)
			err = toml.Unmarshal([]byte("["+table+"]\ncloud-init-runcmds = [\"\\u0000\"]\n"), &cfg)
			require.NoError(t, err)
			require.ErrorContains(t, cfg.Validate(), "null byte")
		})
	}
}
