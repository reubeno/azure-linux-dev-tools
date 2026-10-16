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

func TestCatalogSelectionAndLockFreeSubjectLookup(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.Config.Tests["z"] = pytestTest("z").Definition
	env.Config.Tests["a"] = projectconfig.TestDefinition{Type: "tmt", Description: "smoke"}
	env.Config.TestGroups["pair"] = projectconfig.TestGroup{Tests: []projectconfig.TestRef{{Name: "z"}, {Name: "a"}}}
	env.Config.Components["one"] = projectconfig.ComponentConfig{
		Name: "one", Spec: projectconfig.SpecSource{SourceType: projectconfig.SpecSourceTypeUpstream},
		Tests: &projectconfig.ComponentTestsConfig{Tests: []projectconfig.TestRef{{Group: "pair"}, {Name: "a"}}},
	}
	env.Config.Components["other"] = projectconfig.ComponentConfig{Name: "other"}

	env.Config.Images["base"] = projectconfig.ImageConfig{
		Tests: &projectconfig.ImageTestsConfig{Tests: []projectconfig.TestRef{{Name: "a"}}},
	}
	for _, testcase := range []struct {
		component string
		image     string
		selectors []string
		want      []string
	}{
		{"", "", nil, []string{"a", "z"}},
		{"one", "", nil, []string{"z", "a"}},
		{"", "base", nil, []string{"a"}},
		{"one", "", []string{"a"}, []string{"a"}},
		{"", "", []string{"pair", "z"}, []string{"z", "a"}},
		{"other", "", nil, []string{}},
	} {
		results, err := testexecution.List(env.Env, testcase.component, testcase.image, testcase.selectors)
		require.NoError(t, err)

		names := make([]string, 0, len(results))
		for _, result := range results {
			names = append(names, result.Name)
		}

		assert.Equal(t, testcase.want, names)
	}

	for _, testcase := range []struct {
		component string
		image     string
		selectors []string
		want      string
	}{
		{"one", "base", nil, "only one"},
		{"missing", "", nil, "component not found"},
		{"*", "", nil, "exactly one"},
		{"", "missing", nil, "not defined locally"},
		{"", "", []string{"missing"}, "unknown test selector"},
	} {
		_, err := testexecution.List(env.Env, testcase.component, testcase.image, testcase.selectors)
		require.ErrorContains(t, err, testcase.want)
	}

	broken := env.Config.Components["other"]
	broken.Tests = &projectconfig.ComponentTestsConfig{Tests: []projectconfig.TestRef{{Name: "missing"}}}
	env.Config.Components["other"] = broken
	_, err := testexecution.List(env.Env, "other", "", nil)
	require.Error(t, err)
	assert.Empty(t, env.CommandsExecuted)
}

func TestCatalogAcceptsPublishedOnlyImages(t *testing.T) {
	env := testutils.NewTestEnv(t)
	ref := env.Config.Project.DefaultDistro
	distro := env.Config.Distros[ref.Name]
	version := distro.Versions[ref.Version]
	version.Images = map[string]projectconfig.PublishedImage{"published": {}}
	distro.Versions[ref.Version] = version
	env.Config.Distros[ref.Name] = distro
	results, err := testexecution.List(env.Env, "", "published", nil)
	require.NoError(t, err)
	require.NotNil(t, results)
	assert.Empty(t, results)

	env.Config.Project.DefaultDistro.Name = "missing"
	_, err = testexecution.FindImage(env.Env, "published")
	require.ErrorContains(t, err, "find test image")
}
