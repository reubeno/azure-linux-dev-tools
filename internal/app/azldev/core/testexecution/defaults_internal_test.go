// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func imageDefaults(commands ...string) *projectconfig.ImageTestDefaults {
	return &projectconfig.ImageTestDefaults{
		TMT: projectconfig.TMTImageTestDefaults{CloudInitRuncmds: &commands},
	}
}

func TestImageDefaultsFollowIdentityNotArtifactOrigin(t *testing.T) {
	for _, kind := range []string{componentSubject, imageSubject} {
		for _, origin := range []string{"local", "lkg", "path"} {
			t.Run(kind+"/"+origin, func(t *testing.T) {
				cfg := projectconfig.NewProjectConfig()
				distro := projectconfig.DistroVersionDefinition{
					TestBaseImages: map[string]string{"vm": "base"},
					Images: map[string]projectconfig.PublishedImage{
						"base": {
							Architectures: map[string][]projectconfig.PublishedImageArtifact{
								"x86_64": {{Format: "qcow2", URI: "https://example.test/base.qcow2"}},
							},
							TestDefaults: imageDefaults("echo distro"),
						},
					},
				}
				subject := Subject{Name: "base", Kind: kind, Distro: distro}
				test := projectconfig.ResolvedTest{Name: "smoke", Definition: projectconfig.TestDefinition{
					Type: "tmt", Tmt: map[string]any{"plan": "/plans/smoke"},
				}}

				options := Options{Arch: "x86_64", SourceDir: "/tests"}
				if kind == componentSubject {
					options.BaseImage, options.BaseImageSource = "base", origin
					if origin == "path" {
						options.BaseImagePath = "/base.qcow2"
					}
				} else {
					options.ArtifactSource = origin
					if origin == "path" {
						options.ImagePath = "/base.qcow2"
					}
				}

				check := func(want []string, source string) {
					t.Helper()

					plans, err := planTests(&cfg, subject, []projectconfig.ResolvedTest{test}, options)
					require.NoError(t, err)
					require.Len(t, plans, 1)
					assert.Equal(t, want, plans[0].test.Definition.Tmt["cloud-init-runcmds"])
					assert.Equal(t, source, plans[0].bootstrapSource)
				}

				check([]string{"echo distro"}, "distro-image:base")
				assert.Empty(t, cfg.Images, "a catalog-only image needs no local definition")
				cfg.Images["base"] = projectconfig.ImageConfig{TestDefaults: &projectconfig.ImageTestDefaults{}}

				check([]string{"echo distro"}, "distro-image:base")

				cfg.Images["base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults("echo project")}

				check([]string{"echo project"}, "project-image:base")
				assert.NotContains(t, test.Definition.Tmt, "cloud-init-runcmds", "inherited configuration must be private")
				assert.Equal(t, []string{"echo distro"}, *distro.Images["base"].TestDefaults.TMT.CloudInitRuncmds)

				cfg.Images["base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults([]string{}...)}
				check([]string{}, "project-image:base")
				test.Definition.Tmt["cloud-init-runcmds"] = []string{"echo test"}
				check([]string{"echo test"}, "test:smoke")
				test.Definition.Tmt["cloud-init-runcmds"] = []string{}
				check([]string{}, "test:smoke")
			})
		}
	}
}

func TestAnonymousBaseFileDoesNotBorrowAnImageIdentity(t *testing.T) {
	cfg := projectconfig.NewProjectConfig()
	cfg.Images["base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults("echo project")}
	cfg.Images["explicit-base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults("echo named")}
	subject := Subject{Name: "pkg", Kind: componentSubject, Distro: projectconfig.DistroVersionDefinition{
		TestBaseImages: map[string]string{"vm": "base"},
		Images: map[string]projectconfig.PublishedImage{
			"base": {TestDefaults: imageDefaults("echo distro")},
		},
	}}
	test := projectconfig.ResolvedTest{Name: "smoke", Definition: projectconfig.TestDefinition{
		Type: "tmt", Tmt: map[string]any{"plan": "/plans/smoke"},
		Execution: projectconfig.TestExecutionConfig{BaseImages: map[string]string{"vm": "base"}},
	}}
	options := Options{SourceDir: "/tests", BaseImagePath: "/arbitrary.qcow2"}
	plans, err := planTests(&cfg, subject, []projectconfig.ResolvedTest{test}, options)
	require.NoError(t, err)
	assert.NotContains(t, plans[0].test.Definition.Tmt, "cloud-init-runcmds")
	assert.Empty(t, plans[0].bootstrapSource)

	options.BaseImage = "explicit-base"
	plans, err = planTests(&cfg, subject, []projectconfig.ResolvedTest{test}, options)
	require.NoError(t, err)
	assert.Equal(t, []string{"echo named"}, plans[0].test.Definition.Tmt["cloud-init-runcmds"])
	assert.Equal(t, "project-image:explicit-base", plans[0].bootstrapSource)
}

func TestImageDefaultsOnlyAffectTMTManagedVMs(t *testing.T) {
	for _, testcase := range []struct {
		framework string
		strategy  testframework.Strategy
	}{
		{"tmt", testframework.Host},
		{"tmt", testframework.SSH},
		{"tmt", testframework.Container},
		{"pytest", testframework.Host},
		{"lisa", testframework.VM},
	} {
		t.Run(testcase.framework+"/"+string(testcase.strategy), func(t *testing.T) {
			cfg := projectconfig.NewProjectConfig()
			cfg.Images["base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults("echo should-not-run")}
			plan := plannedTest{
				test: projectconfig.ResolvedTest{Definition: projectconfig.TestDefinition{
					Type: testcase.framework, Tmt: map[string]any{},
				}},
				execution: testframework.ExecutionSpec{Strategy: testcase.strategy},
			}
			require.NoError(t, applyImageDefaults(&cfg, Subject{Name: "base", Kind: imageSubject}, &plan, Options{}))
			assert.Empty(t, plan.test.Definition.Tmt)
			assert.Empty(t, plan.bootstrapSource)
		})
	}
}

func TestEffectiveBootstrapAndOriginAreRetained(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.Config.Images["base"] = projectconfig.ImageConfig{TestDefaults: imageDefaults("echo retained")}
	subject := Subject{Name: "base", Kind: imageSubject}
	tests := []projectconfig.ResolvedTest{{Name: "smoke", Definition: projectconfig.TestDefinition{
		Type: "tmt", Tmt: map[string]any{"plan": "/plans/smoke"},
	}}}
	plans, err := planTests(env.Config, subject, tests, Options{SourceDir: "/tests"})
	require.NoError(t, err)
	request, err := prepareRequest(env.Env, subject, plans[0], testInputs{}, "/work/test")
	require.NoError(t, err)
	data, err := fileutils.ReadFile(env.TestFS, request.InputsPath)
	require.NoError(t, err)

	var manifest struct {
		TMTBootstrapSource string                     `json:"tmtBootstrapSource"`
		Test               projectconfig.ResolvedTest `json:"test"`
	}
	require.NoError(t, json.Unmarshal(data, &manifest))
	assert.Equal(t, "project-image:base", manifest.TMTBootstrapSource)
	assert.Equal(t, []any{"echo retained"}, manifest.Test.Definition.Tmt["cloud-init-runcmds"])

	invalid := []string{"\x00"}
	env.Config.Images["base"].TestDefaults.TMT.CloudInitRuncmds = &invalid
	_, err = planTests(env.Config, subject, tests, Options{SourceDir: "/tests"})
	require.ErrorContains(t, err, "null byte")
}

func TestImplicitNamedBasesInheritTheirOwnDefaults(t *testing.T) {
	subject := Subject{Name: "pkg", Kind: componentSubject, Distro: projectconfig.DistroVersionDefinition{
		TestBaseImages: map[string]string{"vm": "distro-base"},
		Images: map[string]projectconfig.PublishedImage{
			"distro-base": {TestDefaults: imageDefaults("echo distro-base")},
			"test-base":   {TestDefaults: imageDefaults("echo test-base")},
		},
	}}
	test := projectconfig.ResolvedTest{Name: "smoke", Definition: projectconfig.TestDefinition{
		Type: "tmt", Tmt: map[string]any{"plan": "/plans/smoke"},
	}}
	options := Options{SourceDir: "/tests", BaseImageSource: "local"}
	plans, err := planTests(nil, subject, []projectconfig.ResolvedTest{test}, options)
	require.NoError(t, err)
	assert.Equal(t, []string{"echo distro-base"}, plans[0].test.Definition.Tmt["cloud-init-runcmds"])
	test.Definition.Execution.BaseImages = map[string]string{"vm": "test-base"}
	plans, err = planTests(nil, subject, []projectconfig.ResolvedTest{test}, options)
	require.NoError(t, err)
	assert.Equal(t, []string{"echo test-base"}, plans[0].test.Definition.Tmt["cloud-init-runcmds"])
}
