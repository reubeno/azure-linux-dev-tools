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
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				WorkingDir: "tests",
				Args:       []string{"cases/", "--image-path", "{image}"},
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("valid lisa config", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: &projectconfig.LisaConfig{
				RunbookPath:         "/runbooks/basic.yml",
				AdminPrivateKeyPath: "/keys/admin",
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("pytest missing subtable", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "[pytest]")
	})

	t.Run("pytest with lisa subtable", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:   "smoke",
			Type:   projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{},
			Lisa: &projectconfig.LisaConfig{
				RunbookPath:         "/runbooks/basic.yml",
				AdminPrivateKeyPath: "/keys/admin",
			},
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMismatchedTestSubtable)
	})

	t.Run("lisa missing subtable", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "[lisa]")
	})

	t.Run("lisa missing runbook", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: &projectconfig.LisaConfig{
				AdminPrivateKeyPath: "/keys/admin",
			},
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "runbook")
	})

	t.Run("lisa missing admin-private-key-path", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: &projectconfig.LisaConfig{
				RunbookPath: "/runbooks/basic.yml",
			},
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "admin-private-key-path")
	})

	t.Run("lisa with pytest subtable", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: &projectconfig.LisaConfig{
				RunbookPath:         "/runbooks/basic.yml",
				AdminPrivateKeyPath: "/keys/admin",
			},
			Pytest: &projectconfig.PytestConfig{},
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMismatchedTestSubtable)
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
}

func TestTestConfig_MergeUpdatesFrom(t *testing.T) {
	t.Run("merge overrides non-zero fields", func(t *testing.T) {
		base := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				WorkingDir: "tests",
			},
		}
		other := projectconfig.TestConfig{
			Description: "Updated description",
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, "Updated description", base.Description)
		assert.Equal(t, "tests", base.Pytest.WorkingDir)
	})

	t.Run("merge appends args", func(t *testing.T) {
		base := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				Args: []string{"cases/"},
			},
		}
		other := projectconfig.TestConfig{
			Pytest: &projectconfig.PytestConfig{
				Args: []string{"--verbose"},
			},
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, []string{"cases/", "--verbose"}, base.Pytest.Args)
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
					Name: "smoke",
					Type: projectconfig.TestTypePytest,
					Pytest: &projectconfig.PytestConfig{
						WorkingDir: "tests",
					},
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
