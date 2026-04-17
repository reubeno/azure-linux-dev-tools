// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validTestSHA is a 40-character hex string for use in tests.
const validTestSHA = "abcdef0123456789abcdef0123456789abcdef01"

// validLisaConfig returns a valid [projectconfig.LisaConfig] for use in tests.
func validLisaConfig() *projectconfig.LisaConfig {
	return &projectconfig.LisaConfig{
		Framework: projectconfig.GitSourceConfig{
			GitURL: "https://github.com/microsoft/lisa.git",
			Ref:    validTestSHA,
		},
		Runbook: projectconfig.LisaRunbookConfig{
			GitSourceConfig: projectconfig.GitSourceConfig{
				GitURL: "https://github.com/microsoft/azurelinux.git",
				Ref:    validTestSHA,
			},
			Path: "tests/lisa/runbooks/azl-qemu.yml",
		},
	}
}

func TestTestConfig_Validate(t *testing.T) {
	t.Run("valid pytest config", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				WorkingDir: "tests",
				TestPaths:  []string{"cases/"},
				ExtraArgs:  []string{"--image-path", "{image-path}"},
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("valid lisa config", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: validLisaConfig(),
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
			Lisa:   validLisaConfig(),
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

	t.Run("lisa missing framework git-url", func(t *testing.T) {
		cfg := validLisaConfig()
		cfg.Framework.GitURL = ""
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: cfg,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "git-url")
	})

	t.Run("lisa invalid framework ref", func(t *testing.T) {
		cfg := validLisaConfig()
		cfg.Framework.Ref = "not-a-sha"
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: cfg,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrInvalidGitRef)
	})

	t.Run("lisa missing runbook path", func(t *testing.T) {
		cfg := validLisaConfig()
		cfg.Runbook.Path = ""
		testConfig := projectconfig.TestConfig{
			Name: "integration",
			Type: projectconfig.TestTypeLisa,
			Lisa: cfg,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "path")
	})

	t.Run("lisa with pytest subtable", func(t *testing.T) {
		testConfig := projectconfig.TestConfig{
			Name:   "integration",
			Type:   projectconfig.TestTypeLisa,
			Lisa:   validLisaConfig(),
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

	t.Run("merge appends test-paths", func(t *testing.T) {
		base := projectconfig.TestConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				TestPaths: []string{"cases/"},
			},
		}
		other := projectconfig.TestConfig{
			Pytest: &projectconfig.PytestConfig{
				TestPaths: []string{"extra/"},
			},
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, []string{"cases/", "extra/"}, base.Pytest.TestPaths)
	})
}

func TestValidateTestSuiteReferences(t *testing.T) {
	t.Run("valid references", func(t *testing.T) {
		cfg := projectconfig.ProjectConfig{
			Images: map[string]projectconfig.ImageConfig{
				"myimage": {
					Name:  "myimage",
					Tests: projectconfig.ImageTestsConfig{TestSuites: []projectconfig.TestSuiteRef{{Name: "smoke"}}},
				},
			},
			TestSuites: map[string]projectconfig.TestConfig{
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
					Tests: projectconfig.ImageTestsConfig{TestSuites: []projectconfig.TestSuiteRef{{Name: "nonexistent"}}},
				},
			},
			TestSuites:        make(map[string]projectconfig.TestConfig),
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
			TestSuites:        make(map[string]projectconfig.TestConfig),
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		assert.NoError(t, cfg.Validate())
	})
}
