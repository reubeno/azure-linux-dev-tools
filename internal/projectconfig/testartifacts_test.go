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
