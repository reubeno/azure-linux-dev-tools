// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTestConfig_Validate(t *testing.T) {
	t.Run("valid pytest config", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:    "smoke",
			Type:    projectconfig.TestTypePytest,
			TestDir: "/tests/smoke",
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("valid lisa config", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:                "integration",
			Type:                projectconfig.TestTypeLisa,
			RunbookPath:         "/runbooks/basic.yml",
			AdminPrivateKeyPath: "/keys/admin",
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("pytest missing test-dir", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "test-dir")
	})

	t.Run("lisa missing runbook", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:                "integration",
			Type:                projectconfig.TestTypeLisa,
			AdminPrivateKeyPath: "/keys/admin",
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "runbook")
	})

	t.Run("lisa missing admin-private-key-path", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:        "integration",
			Type:        projectconfig.TestTypeLisa,
			RunbookPath: "/runbooks/basic.yml",
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "admin-private-key-path")
	})

	t.Run("unknown test type", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "bad",
			Type: "unknown-type",
		}
		err := testConfig.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrUnknownTestType)
	})

	t.Run("pytest with mock-packages", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:         "extended",
			Type:         projectconfig.TestTypePytest,
			TestDir:      "/tests/extended",
			MockPackages: []string{"libguestfs-tools", "python3-rpm"},
		}
		assert.NoError(t, testConfig.Validate())
	})
}

func TestTestConfig_MergeUpdatesFrom(t *testing.T) {
	t.Run("merge overrides non-zero fields", func(t *testing.T) {
		base := projectconfig.TestConfig{
			Name:    "smoke",
			Type:    projectconfig.TestTypePytest,
			TestDir: "/tests/smoke",
		}
		other := projectconfig.TestConfig{
			Description: "Updated description",
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, "Updated description", base.Description)
		assert.Equal(t, "/tests/smoke", base.TestDir)
	})

	t.Run("merge appends mock-packages", func(t *testing.T) {
		base := projectconfig.TestConfig{
			Name:         "smoke",
			Type:         projectconfig.TestTypePytest,
			TestDir:      "/tests/smoke",
			MockPackages: []string{"python3-rpm"},
		}
		other := projectconfig.TestConfig{
			MockPackages: []string{"libguestfs-tools"},
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, []string{"python3-rpm", "libguestfs-tools"}, base.MockPackages)
	})
}

func TestValidateImageTestReferences(t *testing.T) {
	t.Run("valid references", func(t *testing.T) {
		cfg := projectconfig.ProjectConfig{
			Images: map[string]projectconfig.ImageConfig{
				"myimage": {
					Name:  "myimage",
					Tests: []string{"smoke"},
				},
			},
			Tests: map[string]projectconfig.TestConfig{
				"smoke": {
					Name:    "smoke",
					Type:    projectconfig.TestTypePytest,
					TestDir: "/tests/smoke",
				},
			},
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("undefined test reference", func(t *testing.T) {
		cfg := projectconfig.ProjectConfig{
			Images: map[string]projectconfig.ImageConfig{
				"myimage": {
					Name:  "myimage",
					Tests: []string{"nonexistent"},
				},
			},
			Tests:             make(map[string]projectconfig.TestConfig),
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		err := cfg.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrUndefinedTest)
		assert.Contains(t, err.Error(), "nonexistent")
	})

	t.Run("image with no tests is valid", func(t *testing.T) {
		cfg := projectconfig.ProjectConfig{
			Images: map[string]projectconfig.ImageConfig{
				"myimage": {Name: "myimage"},
			},
			Tests:             make(map[string]projectconfig.TestConfig),
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		assert.NoError(t, cfg.Validate())
	})
}
