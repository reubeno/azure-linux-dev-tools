// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testcmd_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	testcmd "github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/test"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommandWiring(t *testing.T) {
	root := testcmd.NewTestCmd()
	assert.Equal(t, "test", root.Use)
	run, _, err := root.Find([]string{"run"})
	require.NoError(t, err)
	require.NotNil(t, run.RunE)

	for _, name := range []string{"component", "image", "test", "image-path", "rpm", "base-image-path", "execution"} {
		require.NotNil(t, run.Flags().Lookup(name), name)
	}

	assert.Empty(t, run.Flags().Lookup("image-path").Deprecated)

	list, _, err := root.Find([]string{"list"})
	require.NoError(t, err)
	require.NotNil(t, list.RunE)
	assert.Nil(t, list.Flags().Lookup("rebuild"))
	assert.Nil(t, list.Flags().Lookup("discover"))
	assert.Nil(t, list.Flags().Lookup("prepare"))

	_, _, err = root.Find([]string{"discover"})
	require.Error(t, err)
}

func TestSubjectAndFlagErrors(t *testing.T) {
	for _, testcase := range []struct {
		args []string
		want string
	}{
		{[]string{"run"}, "at least one"},
		{[]string{"run", "--component", "pkg", "--image", "base"}, "none of the others"},
		{[]string{"list", "--component", "pkg", "--image", "base"}, "none of the others"},
		{[]string{"run", "--component="}, "must not be empty"},
		{[]string{"list", "--image="}, "must not be empty"},
		{[]string{"run", "pkg"}, "unknown command"},
		{[]string{"list", "--component", "missing"}, "component not found"},
		{[]string{"run", "--component", "missing"}, "component not found"},
		{[]string{"run", "--image", "missing"}, "not defined locally"},
		{[]string{"run", "--component", "pkg", "--image-path", "base.qcow2"}, "use '--base-image-path'"},
		{[]string{"run", "--image", "base", "--rpm", "pkg.rpm"}, "'--rpm' requires"},
		{[]string{"list", "--test", "missing"}, "unknown test selector"},
	} {
		t.Run(testcase.want+":"+testcase.args[0], func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			cmd := testcmd.NewTestCmd()
			cmd.SetArgs(testcase.args)
			require.ErrorContains(t, cmd.ExecuteContext(env.Env), testcase.want)
			assert.Empty(t, env.CommandsExecuted)
		})
	}
}

func TestListIsConfigurationOnlyAndUsesRunSelection(t *testing.T) {
	for _, testcase := range []struct {
		args []string
		want []string
	}{
		{nil, []string{"a", "z"}},
		{[]string{"--component", "pkg"}, []string{"z", "a"}},
		{[]string{"--image", "base"}, []string{"a"}},
		{[]string{"--component", "pkg", "--test", "a"}, []string{"a"}},
		{[]string{"--test", "group", "--test", "z"}, []string{"z", "a"}},
		{[]string{"--component", "empty"}, []string{}},
		{[]string{"--image", "empty"}, []string{}},
		{[]string{"--test", "empty"}, []string{}},
	} {
		t.Run("list "+strings.Join(testcase.args, " "), func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			env.Config.Tests = map[string]projectconfig.TestDefinition{
				"a": {Type: "pytest", Description: "Artifact inspection"},
				"z": {Type: "tmt", Execution: projectconfig.TestExecutionConfig{Strategy: "container"}},
			}
			env.Config.TestGroups["group"] = projectconfig.TestGroup{
				Tests: []projectconfig.TestRef{{Name: "z"}, {Name: "a"}},
			}
			env.Config.TestGroups["empty"] = projectconfig.TestGroup{}
			env.Config.Components["pkg"] = projectconfig.ComponentConfig{
				Name: "pkg", Tests: &projectconfig.ComponentTestsConfig{
					Tests: []projectconfig.TestRef{{Group: "group"}, {Name: "z"}},
				},
			}
			env.Config.Components["empty"] = projectconfig.ComponentConfig{Name: "empty"}
			env.Config.Images["base"] = projectconfig.ImageConfig{
				Tests: &projectconfig.ImageTestsConfig{Tests: []projectconfig.TestRef{{Name: "a"}}},
			}
			env.Config.Images["empty"] = projectconfig.ImageConfig{}
			env.Env.SetDefaultReportFormat(azldev.ReportFormatJSON)

			var output bytes.Buffer
			env.Env.SetReportFile(&output)

			cmd := testcmd.NewListCmd()
			cmd.SetArgs(testcase.args)
			require.NoError(t, cmd.ExecuteContext(env.Env))

			var results []testexecution.TestInfo
			require.NoError(t, json.Unmarshal(output.Bytes(), &results))
			require.NotNil(t, results, "empty catalogs serialize as [], not null")

			names := make([]string, 0, len(results))
			for _, result := range results {
				names = append(names, result.Name)
				if result.Name == "a" {
					assert.Equal(t, "host", result.Strategy)
					assert.Equal(t, "Artifact inspection", result.Description)
				} else {
					assert.Equal(t, "container", result.Strategy)
				}
			}

			assert.Equal(t, testcase.want, names)
			assert.Empty(t, env.CommandsExecuted, "even unprepared test definitions can be listed")
		})
	}
}

func TestRunDispatchesExplicitArtifactsWithoutPackagingLocks(t *testing.T) {
	for _, kind := range []string{"component", "image"} {
		t.Run(kind, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			env.Config.Tests["smoke"] = projectconfig.TestDefinition{Type: "pytest", Pytest: map[string]any{
				"working-dir": "/project/tests", "test-paths": []string{"test_image.py"},
			}}
			refs := []projectconfig.TestRef{{Name: "smoke"}}
			env.Config.Components["candidate"] = projectconfig.ComponentConfig{
				Name: "candidate", Spec: projectconfig.SpecSource{SourceType: projectconfig.SpecSourceTypeUpstream},
				Tests: &projectconfig.ComponentTestsConfig{Tests: refs},
			}

			env.Config.Images["candidate"] = projectconfig.ImageConfig{
				Tests: &projectconfig.ImageTestsConfig{Tests: refs},
			}
			options := azldev.NewEnvOptions()
			options.Config, options.ProjectDir = env.Config, env.Env.ProjectDir()
			options.Interfaces, options.EventListener = env.TestInterfaces, env.EventListener
			options.DryRunnable = azldev.NewAppDryRunnable(true)
			dryEnv := azldev.NewEnv(t.Context(), options)
			dryEnv.SetDefaultReportFormat(azldev.ReportFormatJSON)

			var output bytes.Buffer
			dryEnv.SetReportFile(&output)

			path, flag := "/project/input.qcow2", "--image-path"
			if kind == "component" {
				path, flag = "/project/input.rpm", "--rpm"
			}

			require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("candidate"), fileperms.PrivateFile))

			cmd := testcmd.NewRunCmd()
			cmd.SetArgs([]string{"--" + kind, "candidate", flag, path, "--arch", "x86_64"})
			require.NoError(t, cmd.ExecuteContext(dryEnv))

			var results []testframework.RunResult
			require.NoError(t, json.Unmarshal(output.Bytes(), &results))
			require.Len(t, results, 1)
			assert.Equal(t, "smoke", results[0].Name)
			assert.Equal(t, testframework.Planned, results[0].Status)
			assert.Empty(t, env.CommandsExecuted)
		})
	}
}

func TestListPublishedOnlyImageAndEmptyCatalog(t *testing.T) {
	env := testutils.NewTestEnv(t)
	results, err := testexecution.List(env.Env, "", "", nil)
	require.NoError(t, err)
	assert.Empty(t, results)
	require.NotNil(t, results)

	_, err = testexecution.List(env.Env, "component", "image", nil)
	require.ErrorContains(t, err, "only one")

	distroName := env.Config.Project.DefaultDistro.Name
	versionName := env.Config.Project.DefaultDistro.Version
	distro := env.Config.Distros[distroName]
	version := distro.Versions[versionName]
	version.Images = map[string]projectconfig.PublishedImage{"published": {}}
	distro.Versions[versionName] = version
	env.Config.Distros[distroName] = distro
	env.Config.Tests["smoke"] = projectconfig.TestDefinition{Type: "tmt"}
	results, err = testexecution.List(env.Env, "", "published", []string{"smoke"})
	require.NoError(t, err)
	assert.Equal(t, []testexecution.TestInfo{{Name: "smoke", Framework: "tmt", Strategy: "vm"}}, results)
	assert.Empty(t, env.Config.Images)
	assert.Empty(t, env.CommandsExecuted)
}
