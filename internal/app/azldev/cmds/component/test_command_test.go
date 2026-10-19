// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package component_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/component"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComponentTestCommandFlags(t *testing.T) {
	cmd := component.NewComponentTestCmd()
	assert.Equal(t, "test COMPONENT", cmd.Use)
	require.NotNil(t, cmd.RunE)

	for _, flag := range []string{
		"test", "execution", "artifact-source", "rebuild", "rpm", "base-image",
		"base-image-path", "base-image-source", "rebuild-base-image", "ssh-host", "ssh-user", "junit-xml",
	} {
		require.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}

	assert.Empty(t, cmd.Flags().Lookup("rpm").Annotations["cobra_annotation_bash_completion_one_required_flag"])
}

func TestComponentTestNoMatch(t *testing.T) {
	env := testutils.NewTestEnv(t)
	cmd := component.NewComponentTestCmd()
	cmd.SetArgs([]string{"does-not-exist"})
	require.ErrorContains(t, cmd.ExecuteContext(env.Env), "component not found")
	assert.Empty(t, env.CommandsExecuted)
}

func TestComponentTestFrameworksWithoutPackagingLocks(t *testing.T) {
	for _, framework := range []string{"tmt", "lisa", "pytest"} {
		t.Run(framework, func(t *testing.T) {
			env := testutils.NewTestEnv(t)
			env.Config.Tests["tmt"] = projectconfig.TestDefinition{Type: "tmt", Tmt: map[string]any{
				"plan": "/plans/smoke",
			}}
			env.Config.Tests["lisa"] = projectconfig.TestDefinition{Type: "lisa", Lisa: map[string]any{
				"testcase-name": "smoke",
			}}
			env.Config.Tests["pytest"] = projectconfig.TestDefinition{Type: "pytest", Pytest: map[string]any{
				"working-dir": "/project/tests", "test-paths": []string{"test_artifact.py"},
			}}
			env.Config.TestGroups["selected"] = projectconfig.TestGroup{
				Tests: []projectconfig.TestRef{{Name: framework}},
			}

			env.Config.Components["candidate"] = projectconfig.ComponentConfig{
				Name: "candidate", Spec: projectconfig.SpecSource{SourceType: projectconfig.SpecSourceTypeUpstream},
				Tests: &projectconfig.ComponentTestsConfig{Tests: []projectconfig.TestRef{{Name: "pytest"}}},
			}
			for _, path := range []string{"/project/input.rpm", "/project/base.qcow2"} {
				require.NoError(t, fileutils.WriteFile(env.TestFS, path, []byte("candidate"), fileperms.PrivateFile))
			}

			options := azldev.NewEnvOptions()
			options.Config, options.ProjectDir = env.Config, env.Env.ProjectDir()
			options.Interfaces, options.EventListener = env.TestInterfaces, env.EventListener
			options.DryRunnable = azldev.NewAppDryRunnable(true)
			dryEnv := azldev.NewEnv(t.Context(), options)
			dryEnv.SetDefaultReportFormat(azldev.ReportFormatJSON)

			var output bytes.Buffer
			dryEnv.SetReportFile(&output)

			args := []string{
				"candidate", "--rpm", "/project/input.rpm", "--arch", "x86_64",
				"--test", "selected", "--test", framework,
			}
			switch framework {
			case "tmt":
				args = append(args, "--source-dir", "/project/tests", "--base-image-path", "/project/base.qcow2")
			case "lisa":
				args = append(args, "--lisa-dir", "/project/lisa", "--base-image-path", "/project/base.qcow2")
			}

			cmd := component.NewComponentTestCmd()
			cmd.SetArgs(args)
			require.NoError(t, cmd.ExecuteContext(dryEnv))

			var results []testframework.RunResult
			require.NoError(t, json.Unmarshal(output.Bytes(), &results))
			require.Len(t, results, 1, "explicit groups replace associations and duplicate selections run once")
			assert.Equal(t, framework, results[0].Name)
			assert.Equal(t, framework, results[0].Framework)
			assert.Equal(t, testframework.Planned, results[0].Status)
			assert.Empty(t, env.CommandsExecuted)
		})
	}
}
