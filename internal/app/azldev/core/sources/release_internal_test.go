// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package sources

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/components/components_testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const testSourcesDir = "/sources"

func newTestPreparer(memFS afero.Fs) *sourcePreparerImpl {
	return &sourcePreparerImpl{
		fs: memFS,
	}
}

func writeTestSpec(t *testing.T, memFS afero.Fs, name, release string) {
	t.Helper()

	specDir := filepath.Join(testSourcesDir, name)
	require.NoError(t, fileutils.MkdirAll(memFS, specDir))

	specPath := filepath.Join(specDir, name+".spec")
	content := []byte("Name: " + name + "\nVersion: 1.0.0\nRelease: " + release + "\nSummary: Test\nLicense: MIT\n")

	require.NoError(t, fileutils.WriteFile(memFS, specPath, content, fileperms.PublicFile))
}

func mockComponent(
	ctrl *gomock.Controller, name string, config *projectconfig.ComponentConfig,
) *components_testutils.MockComponent {
	comp := components_testutils.NewMockComponent(ctrl)
	comp.EXPECT().GetName().AnyTimes().Return(name)
	comp.EXPECT().GetConfig().AnyTimes().Return(config)

	return comp
}

func TestTryBumpStaticRelease_ManualSkips(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	comp := mockComponent(ctrl, "kernel", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationManual,
		},
	})

	// No spec file needed — should skip before reading anything.
	err := preparer.tryBumpStaticRelease(comp, testSourcesDir, 3)
	require.NoError(t, err)
}

func TestTryBumpStaticRelease_AutoreleaseSkips(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "%autorelease")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAuto,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 3)
	require.NoError(t, err)
}

func TestTryBumpStaticRelease_StaticBumps(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "1%{?dist}")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAuto,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 3)
	require.NoError(t, err)

	// Verify the spec was updated.
	specPath := filepath.Join(testSourcesDir, "test-pkg", "test-pkg.spec")
	content, err := fileutils.ReadFile(memFS, specPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Release: 4%{?dist}")
}

func TestTryBumpStaticRelease_StaticBumpsNonConditionalDist(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "1%{dist}")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAuto,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 3)
	require.NoError(t, err)

	specPath := filepath.Join(testSourcesDir, "test-pkg", "test-pkg.spec")
	content, err := fileutils.ReadFile(memFS, specPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Release: 4%{dist}")
}

func TestTryBumpStaticRelease_NonStandardErrorsWithoutManual(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "kernel", "%{pkg_release}")

	comp := mockComponent(ctrl, "kernel", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAuto,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "kernel"), 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be bumped")
	assert.Contains(t, err.Error(), "release.calculation")
}

func TestTryBumpStaticRelease_NonStandardSucceedsWithManual(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "kernel", "%{pkg_release}")

	comp := mockComponent(ctrl, "kernel", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationManual,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "kernel"), 3)
	require.NoError(t, err)
}

func TestTryBumpStaticRelease_ExplicitAutoreleaseSkips(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	// Spec has a static release, but config says autorelease — should skip.
	comp := mockComponent(ctrl, "gvisor", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAutorelease,
		},
	})

	// No spec file needed — should skip before reading anything.
	err := preparer.tryBumpStaticRelease(comp, testSourcesDir, 3)
	require.NoError(t, err)
}

func TestTryBumpStaticRelease_ExplicitStaticBumps(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "1%{?dist}")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationStatic,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 3)
	require.NoError(t, err)

	// Verify the spec was updated.
	specPath := filepath.Join(testSourcesDir, "test-pkg", "test-pkg.spec")
	content, err := fileutils.ReadFile(memFS, specPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Release: 4%{?dist}")
}

func TestTryBumpStaticRelease_ExplicitStaticErrorsOnAutorelease(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	// Spec uses %autorelease, but config says static — should error.
	writeTestSpec(t, memFS, "test-pkg", "%autorelease")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationStatic,
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `release.calculation = "autorelease"`)
}

func TestBumpReleaseWithRegex(t *testing.T) {
	t.Run("increments selected counter", func(t *testing.T) {
		actual, err := BumpReleaseWithRegex(
			"0.044.git20261010%{?dist}",
			`^0\.([0-9]+)(?:\.git.*)$`,
			2,
		)
		require.NoError(t, err)
		assert.Equal(t, "0.046.git20261010%{?dist}", actual)
	})

	t.Run("zero increment preserves value", func(t *testing.T) {
		actual, err := BumpReleaseWithRegex("007%{?dist}", `^([0-9]+)(?:%\{\?dist\})$`, 0)
		require.NoError(t, err)
		assert.Equal(t, "007%{?dist}", actual)
	})

	t.Run("rejects negative increment", func(t *testing.T) {
		_, err := BumpReleaseWithRegex("1", `^([0-9]+)$`, -1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be negative")
	})

	t.Run("requires match from beginning", func(t *testing.T) {
		_, err := BumpReleaseWithRegex("release-44", `([0-9]+)$`, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "full Release value")
	})

	t.Run("requires match through end", func(t *testing.T) {
		_, err := BumpReleaseWithRegex("0.44.git", `^0\.([0-9]+)`, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "full Release value")
	})

	t.Run("requires integral capture", func(t *testing.T) {
		_, err := BumpReleaseWithRegex("release-12a", `^release-(.*)$`, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsigned decimal")
	})

	t.Run("requires participating capture", func(t *testing.T) {
		_, err := BumpReleaseWithRegex("fallback", `^(?:release-([0-9]+)|fallback)$`, 1)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did not capture")
	})
}

func TestGetReleaseTagValue_RejectsMultipleMainPackageTags(t *testing.T) {
	memFS := afero.NewMemMapFs()
	specPath := filepath.Join(testSourcesDir, "conditional.spec")
	require.NoError(t, fileutils.WriteFile(
		memFS,
		specPath,
		[]byte("Name: conditional\nVersion: 1\nRelease: 1%{?dist}\nRelease: 2%{?dist}\n"),
		fileperms.PublicFile,
	))

	_, err := GetReleaseTagValue(memFS, specPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains 2 main-package Release tags")
}

func TestTryBumpStaticRelease_ReleaseTagCounterSelectsOneConditionalTag(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	specDir := filepath.Join(testSourcesDir, "test-pkg")
	require.NoError(t, fileutils.MkdirAll(memFS, specDir))

	specPath := filepath.Join(specDir, "test-pkg.spec")
	require.NoError(t, fileutils.WriteFile(
		memFS,
		specPath,
		[]byte("Name: test-pkg\nVersion: 1\nRelease: 4%{?dist}\nRelease: 0.@PACKAGE_RELEASE@%{?dist}.26\n"),
		fileperms.PublicFile,
	))

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationStatic,
			Counter: &projectconfig.ReleaseCounterConfig{
				Source: projectconfig.ReleaseCounterSourceReleaseTag,
				Regex:  `^([0-9]+)%\{\?dist\}$`,
			},
		},
	})

	err := preparer.tryBumpStaticRelease(comp, specDir, 1)
	require.NoError(t, err)

	content, err := fileutils.ReadFile(memFS, specPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Release: 5%{?dist}")
	assert.Contains(t, string(content), "Release: 0.@PACKAGE_RELEASE@%{?dist}.26")
}

func TestTryBumpStaticRelease_CustomReleaseTagCounter(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "0.44.git20261010%{?dist}")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationStatic,
			Counter: &projectconfig.ReleaseCounterConfig{
				Source: projectconfig.ReleaseCounterSourceReleaseTag,
				Regex:  `^0\.([0-9]+)(?:\.git.*)$`,
			},
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 2)
	require.NoError(t, err)

	specPath := filepath.Join(testSourcesDir, "test-pkg", "test-pkg.spec")
	content, err := fileutils.ReadFile(memFS, specPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Release: 0.46.git20261010%{?dist}")
}

func TestTryBumpStaticRelease_SpecMacroCounter(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		directive projectconfig.ReleaseCounterDirective
	}{
		{name: "global", directive: projectconfig.ReleaseCounterDirectiveGlobal},
		{name: "define", directive: projectconfig.ReleaseCounterDirectiveDefine},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			memFS := afero.NewMemMapFs()
			preparer := newTestPreparer(memFS)

			specDir := filepath.Join(testSourcesDir, "test-pkg")
			require.NoError(t, fileutils.MkdirAll(memFS, specDir))

			specPath := filepath.Join(specDir, "test-pkg.spec")
			specContents := fmt.Sprintf(
				"%%%s baserelease 44\nName: test-pkg\nVersion: 1.0.0\n"+
					"Release: %%{baserelease}%%{?dist}\nRelease: 0.%%{baserelease}.beta%%{?dist}\n",
				testCase.directive,
			)
			require.NoError(t, fileutils.WriteFile(
				memFS, specPath, []byte(specContents), fileperms.PublicFile,
			))

			comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
				Release: projectconfig.ReleaseConfig{
					Calculation: projectconfig.ReleaseCalculationStatic,
					Counter: &projectconfig.ReleaseCounterConfig{
						Source:    projectconfig.ReleaseCounterSourceSpecMacro,
						Directive: testCase.directive,
						Name:      "baserelease",
					},
				},
			})

			err := preparer.tryBumpStaticRelease(comp, specDir, 2)
			require.NoError(t, err)

			content, err := fileutils.ReadFile(memFS, specPath)
			require.NoError(t, err)
			assert.Contains(t, string(content), fmt.Sprintf("%%%s baserelease 46", testCase.directive))
			assert.Contains(t, string(content), "Release: %{baserelease}%{?dist}")
			assert.Contains(t, string(content), "Release: 0.%{baserelease}.beta%{?dist}")
		})
	}
}

func TestTryBumpStaticRelease_AutoAutoreleaseIgnoresCounterFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	memFS := afero.NewMemMapFs()
	preparer := newTestPreparer(memFS)

	writeTestSpec(t, memFS, "test-pkg", "%autorelease")

	comp := mockComponent(ctrl, "test-pkg", &projectconfig.ComponentConfig{
		Release: projectconfig.ReleaseConfig{
			Calculation: projectconfig.ReleaseCalculationAuto,
			Counter: &projectconfig.ReleaseCounterConfig{
				Source: projectconfig.ReleaseCounterSourceReleaseTag,
				Regex:  `^([0-9]+)$`,
			},
		},
	})

	err := preparer.tryBumpStaticRelease(comp, filepath.Join(testSourcesDir, "test-pkg"), 1)
	require.NoError(t, err)
}
