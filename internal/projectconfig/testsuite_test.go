// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package projectconfig_test

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageCapabilities_EnabledNames(t *testing.T) {
	t.Run("all enabled", func(t *testing.T) {
		caps := projectconfig.ImageCapabilities{
			MachineBootable:          lo.ToPtr(true),
			Container:                lo.ToPtr(true),
			Systemd:                  lo.ToPtr(true),
			RuntimePackageManagement: lo.ToPtr(true),
		}
		assert.Equal(t, []string{
			"machine-bootable", "container", "systemd", "runtime-package-management",
		}, caps.EnabledNames())
	})

	t.Run("partial enabled", func(t *testing.T) {
		caps := projectconfig.ImageCapabilities{
			Container: lo.ToPtr(true),
			Systemd:   lo.ToPtr(true),
		}
		assert.Equal(t, []string{"container", "systemd"}, caps.EnabledNames())
	})

	t.Run("explicitly false excluded", func(t *testing.T) {
		caps := projectconfig.ImageCapabilities{
			MachineBootable: lo.ToPtr(false),
			Container:       lo.ToPtr(true),
		}
		assert.Equal(t, []string{"container"}, caps.EnabledNames())
	})

	t.Run("all nil returns nil", func(t *testing.T) {
		caps := projectconfig.ImageCapabilities{}
		assert.Nil(t, caps.EnabledNames())
	})
}

func TestImageConfig_TestNames(t *testing.T) {
	t.Run("with tests", func(t *testing.T) {
		img := projectconfig.ImageConfig{
			Tests: projectconfig.ImageTestsConfig{
				TestSuites: []projectconfig.TestSuiteRef{
					{Name: "smoke"},
					{Name: "integration"},
				},
			},
		}
		assert.Equal(t, []string{"smoke", "integration"}, img.TestNames())
	})

	t.Run("no tests returns empty", func(t *testing.T) {
		img := projectconfig.ImageConfig{}
		assert.Empty(t, img.TestNames())
	})
}

func TestTestSuiteConfig_Validate(t *testing.T) {
	t.Run("valid pytest config", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
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

	t.Run("valid pytest config with install mode", func(t *testing.T) {
		for _, mode := range []projectconfig.PytestInstallMode{
			projectconfig.PytestInstallPyproject,
			projectconfig.PytestInstallRequirements,
			projectconfig.PytestInstallNone,
		} {
			t.Run(string(mode), func(t *testing.T) {
				testConfig := projectconfig.TestSuiteConfig{
					Name: "smoke",
					Type: projectconfig.TestTypePytest,
					Pytest: &projectconfig.PytestConfig{
						Install:    mode,
						WorkingDir: "tests",
					},
				}
				assert.NoError(t, testConfig.Validate())
			})
		}
	})

	t.Run("valid pytest config with empty install mode", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				WorkingDir: "tests",
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("invalid install mode", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				Install: "bad-mode",
			},
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrInvalidInstallMode)
		assert.Contains(t, err.Error(), "bad-mode")
	})

	t.Run("install mode requires working-dir", func(t *testing.T) {
		for _, mode := range []projectconfig.PytestInstallMode{
			projectconfig.PytestInstallPyproject,
			projectconfig.PytestInstallRequirements,
		} {
			t.Run(string(mode), func(t *testing.T) {
				testConfig := projectconfig.TestSuiteConfig{
					Name: "smoke",
					Type: projectconfig.TestTypePytest,
					Pytest: &projectconfig.PytestConfig{
						Install: mode,
						// WorkingDir intentionally omitted.
					},
				}
				err := testConfig.Validate()
				require.Error(t, err)
				require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
				assert.Contains(t, err.Error(), "working-dir")
			})
		}
	})

	t.Run("install none without working-dir is valid", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				Install: projectconfig.PytestInstallNone,
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("default install without working-dir is valid", func(t *testing.T) {
		// Default mode is 'none' (no install) and so doesn't require working-dir.
		testConfig := projectconfig.TestSuiteConfig{
			Name:   "smoke",
			Type:   projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				// Both Install and WorkingDir omitted.
			},
		}
		assert.NoError(t, testConfig.Validate())
	})

	t.Run("pytest missing subtable", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "[pytest]")
	})

	t.Run("unknown test type", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "bad",
			Type: "unknown-type",
		}
		err := testConfig.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrUnknownTestType)
	})

	t.Run("missing type returns missing-field error", func(t *testing.T) {
		testConfig := projectconfig.TestSuiteConfig{
			Name: "smoke",
			// Type intentionally omitted.
		}
		err := testConfig.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrMissingTestField)
		assert.Contains(t, err.Error(), "type")
	})
}

const validSHA = "a6bcc6767229199f4f02b781d1d39df0835d894b"

func tmtSuite(mods func(*projectconfig.TmtConfig)) *projectconfig.TestSuiteConfig {
	cfg := &projectconfig.TmtConfig{
		Source: projectconfig.TmtGitSource{
			GitURL: "https://example.com/repo.git",
			Ref:    validSHA,
		},
		Plan: "/plans/smoke",
	}
	if mods != nil {
		mods(cfg)
	}

	return &projectconfig.TestSuiteConfig{
		Name: "tmt-smoke",
		Type: projectconfig.TestTypeTmt,
		Tmt:  cfg,
	}
}

func TestTmtConfig_Validate(t *testing.T) {
	t.Run("minimal valid config passes", func(t *testing.T) {
		s := tmtSuite(nil)
		assert.NoError(t, s.Validate())
	})

	t.Run("with optional how=virtual passes", func(t *testing.T) {
		s := tmtSuite(func(c *projectconfig.TmtConfig) {
			c.Provision.How = projectconfig.TmtProvisionHowVirtual
		})
		assert.NoError(t, s.Validate())
	})

	t.Run("missing tmt subtable when type=tmt", func(t *testing.T) {
		suite := &projectconfig.TestSuiteConfig{Name: "x", Type: projectconfig.TestTypeTmt}
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMissingTestField)
	})

	t.Run("type=tmt rejects pytest subtable", func(t *testing.T) {
		suite := tmtSuite(nil)
		suite.Pytest = &projectconfig.PytestConfig{}
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMismatchedTestSubtable)
	})

	t.Run("type=pytest rejects tmt subtable", func(t *testing.T) {
		suite := &projectconfig.TestSuiteConfig{
			Name:   "x",
			Type:   projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{},
			Tmt:    &projectconfig.TmtConfig{},
		}
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMismatchedTestSubtable)
	})

	t.Run("missing source.git-url", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) { c.Source.GitURL = "" })
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMissingTestField)
	})

	t.Run("missing source.ref", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) { c.Source.Ref = "" })
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMissingTestField)
	})

	t.Run("non-40-char ref rejected", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) { c.Source.Ref = "abc1234" })
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrInvalidGitRef)
	})

	t.Run("non-hex ref rejected", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) {
			c.Source.Ref = "ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
		})
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrInvalidGitRef)
	})

	t.Run("missing plan", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) { c.Plan = "" })
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrMissingTestField)
	})

	t.Run("unsupported provision how", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) {
			c.Provision.How = "container"
		})
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrUnsupportedTmtProvision)
	})

	t.Run("forbidden flag in run-extra-args", func(t *testing.T) {
		for _, bad := range []string{"--id", "-i", "-c", "--context", "--id=foo", "-c=k=v"} {
			t.Run(bad, func(t *testing.T) {
				suite := tmtSuite(func(c *projectconfig.TmtConfig) {
					c.RunExtraArgs = []string{bad}
				})
				err := suite.Validate()
				require.Error(t, err)
				assert.ErrorIs(t, err, projectconfig.ErrForbiddenExtraArg)
			})
		}
	})

	t.Run("forbidden --image in provision-extra-args", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) {
			c.ProvisionExtraArgs = []string{"--image=/somewhere/else.qcow2"}
		})
		err := suite.Validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, projectconfig.ErrForbiddenExtraArg)
	})

	t.Run("plan-extra-args is unrestricted", func(t *testing.T) {
		suite := tmtSuite(func(c *projectconfig.TmtConfig) {
			c.PlanExtraArgs = []string{"--filter", "tag:smoke"}
		})
		assert.NoError(t, suite.Validate())
	})
}

func TestTestSuiteConfig_WithAbsolutePaths_TmtDeepCopies(t *testing.T) {
	src := tmtSuite(func(c *projectconfig.TmtConfig) {
		c.PipExtras = []string{"x"}
		c.RunExtraArgs = []string{"-vvv"}
		c.Context = map[string][]string{"distro": {"a", "b"}}
	})
	got := src.WithAbsolutePaths("/anywhere")
	require.NotNil(t, got.Tmt)
	// Mutating the copy must not bleed back.
	got.Tmt.PipExtras[0] = "MUTATED"
	got.Tmt.Context["distro"][0] = "MUTATED"
	assert.Equal(t, "x", src.Tmt.PipExtras[0])
	assert.Equal(t, "a", src.Tmt.Context["distro"][0])
}

func TestPytestConfig_EffectiveInstallMode(t *testing.T) {
	t.Run("default is none", func(t *testing.T) {
		cfg := &projectconfig.PytestConfig{}
		assert.Equal(t, projectconfig.PytestInstallNone, cfg.EffectiveInstallMode())
	})

	t.Run("explicit mode is preserved", func(t *testing.T) {
		cfg := &projectconfig.PytestConfig{Install: projectconfig.PytestInstallPyproject, WorkingDir: "tests"}
		assert.Equal(t, projectconfig.PytestInstallPyproject, cfg.EffectiveInstallMode())
	})

	t.Run("requirements mode", func(t *testing.T) {
		cfg := &projectconfig.PytestConfig{Install: projectconfig.PytestInstallRequirements}
		assert.Equal(t, projectconfig.PytestInstallRequirements, cfg.EffectiveInstallMode())
	})
}

func TestTestSuiteConfig_MergeUpdatesFrom(t *testing.T) {
	t.Run("merge overrides non-zero fields", func(t *testing.T) {
		base := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				WorkingDir: "tests",
			},
		}
		other := projectconfig.TestSuiteConfig{
			Description: "Updated description",
		}
		require.NoError(t, base.MergeUpdatesFrom(&other))
		assert.Equal(t, "Updated description", base.Description)
		assert.Equal(t, "tests", base.Pytest.WorkingDir)
	})

	t.Run("merge appends test-paths", func(t *testing.T) {
		base := projectconfig.TestSuiteConfig{
			Name: "smoke",
			Type: projectconfig.TestTypePytest,
			Pytest: &projectconfig.PytestConfig{
				TestPaths: []string{"cases/"},
			},
		}
		other := projectconfig.TestSuiteConfig{
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
			TestSuites: map[string]projectconfig.TestSuiteConfig{
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
			TestSuites:        make(map[string]projectconfig.TestSuiteConfig),
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		err := cfg.Validate()
		require.Error(t, err)
		require.ErrorIs(t, err, projectconfig.ErrUndefinedTestSuite)
		assert.Contains(t, err.Error(), "nonexistent")
	})

	t.Run("image with no tests is valid", func(t *testing.T) {
		cfg := projectconfig.ProjectConfig{
			Images: map[string]projectconfig.ImageConfig{
				"myimage": {Name: "myimage"},
			},
			TestSuites:        make(map[string]projectconfig.TestSuiteConfig),
			Components:        make(map[string]projectconfig.ComponentConfig),
			ComponentGroups:   make(map[string]projectconfig.ComponentGroupConfig),
			Distros:           make(map[string]projectconfig.DistroDefinition),
			GroupsByComponent: make(map[string][]string),
			PackageGroups:     make(map[string]projectconfig.PackageGroupConfig),
		}
		assert.NoError(t, cfg.Validate())
	})
}
