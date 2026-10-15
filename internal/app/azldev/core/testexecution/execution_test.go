// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package testexecution_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/artifacts"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testexecution"
	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/core/testutils"
	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pytestTest(name string) projectconfig.ResolvedTest {
	return projectconfig.ResolvedTest{Name: name, Definition: projectconfig.TestDefinition{
		Type: "pytest", Pytest: map[string]any{
			"working-dir": "/project/tests", "test-paths": []string{"test_example.py"}, "install": "none",
		},
	}}
}

func TestSelectorsRejectEveryUnknownAndDedupe(t *testing.T) {
	cfg := projectconfig.NewProjectConfig()
	cfg.Tests["one"] = pytestTest("one").Definition
	cfg.TestGroups["group"] = projectconfig.TestGroup{Tests: []projectconfig.TestRef{{Name: "one"}}}
	tests, err := testexecution.Select(&cfg, nil, []string{"one", "group"})
	require.NoError(t, err)
	require.Len(t, tests, 1)

	_, err = testexecution.Select(&cfg, nil, []string{"one", "missing"})
	require.ErrorContains(t, err, "unknown test selector")
}

func TestPreflightPrecedesArtifactBuild(t *testing.T) {
	env := testutils.NewTestEnv(t)
	built := false
	resolver := &artifacts.Resolver{Env: env.Env, BuildPackages: func(_, _ string) ([]string, error) {
		built = true

		return nil, nil
	}}
	tests := []projectconfig.ResolvedTest{
		pytestTest("first"),
		{Name: "unsupported", Definition: projectconfig.TestDefinition{Type: "unknown"}},
	}
	_, err := testexecution.Run(env.Env, testexecution.Subject{Name: "curl", Kind: "component"}, tests,
		testexecution.Options{}, resolver)
	require.ErrorContains(t, err, "unsupported test framework")
	assert.False(t, built)
	assert.Empty(t, env.CommandsExecuted)
}

func TestComponentPytestInputsAndAggregatedJUnit(t *testing.T) {
	env := testutils.NewTestEnv(t)
	env.CmdFactory.RegisterCommandInSearchPath("python3")
	require.NoError(t, fileutils.MkdirAll(env.TestFS, "/project/tests"))
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/project/pkg.rpm", []byte("candidate"), fileperms.PrivateFile))
	require.NoError(t, fileutils.WriteFile(env.TestFS, "/project/tests/requirements.txt",
		[]byte("pytest\n"), fileperms.PrivateFile))

	var reportPaths []string

	env.CmdFactory.RunHandler = func(cmd *exec.Cmd) error {
		if slices.Contains(cmd.Args, "venv") {
			dir := cmd.Args[len(cmd.Args)-1]
			require.NoError(t, fileutils.MkdirAll(env.TestFS, filepath.Join(dir, "bin")))
			require.NoError(t, fileutils.WriteFile(env.TestFS, filepath.Join(dir, "bin", "python3"),
				nil, fileperms.PrivateFile))
			require.NoError(t, fileutils.WriteFile(env.TestFS, filepath.Join(dir, "pyvenv.cfg"),
				[]byte("home = /python\n"), fileperms.PrivateFile))
		}

		if slices.Contains(cmd.Args, "pytest") {
			for index, arg := range cmd.Args {
				if arg == "--junit-xml" || arg == "--junitxml" {
					path := cmd.Args[index+1]
					reportPaths = append(reportPaths, path)

					return fileutils.WriteFile(env.TestFS, path,
						[]byte(`<testsuites><testsuite name="native" tests="1"><testcase name="case"/></testsuite></testsuites>`),
						fileperms.PrivateFile)
				}
			}

			t.Fatal("pytest was not passed its per-test report destination")
		}

		return nil
	}

	tests := []projectconfig.ResolvedTest{pytestTest("one"), pytestTest("two")}
	for _, test := range tests {
		test.Definition.Pytest["install"] = "requirements"
	}

	results, err := testexecution.Run(env.Env, testexecution.Subject{Name: "curl", Kind: "component"},
		tests,
		testexecution.Options{RPMPaths: []string{"pkg.rpm"}, JUnitXMLPath: "results.xml"},
		&artifacts.Resolver{Env: env.Env})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, testframework.Passed, results[0].Status)
	require.Len(t, reportPaths, 2)
	assert.NotEqual(t, reportPaths[0], reportPaths[1])

	report, err := fileutils.ReadFile(env.TestFS, "/project/results.xml")
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(report), "<testcase"))

	inputs, err := fileutils.ReadFile(env.TestFS, filepath.Join(results[0].OutputDir, "inputs.json"))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(inputs, &decoded))
	assert.Equal(t, "curl", decoded["subject"])
	assert.Contains(t, string(inputs), "/project/pkg.rpm")
}

func TestImageTMTDoesNotAcceptUnrelatedSSHTarget(t *testing.T) {
	env := testutils.NewTestEnv(t)
	test := projectconfig.ResolvedTest{Name: "tmt", Definition: projectconfig.TestDefinition{
		Type: "tmt", Tmt: map[string]any{"source": map[string]any{
			"git-url": "https://example.test/tests.git", "ref": strings.Repeat("a", 40),
		}},
	}}
	_, err := testexecution.Run(env.Env, testexecution.Subject{Name: "base", Kind: "image"},
		[]projectconfig.ResolvedTest{test},
		testexecution.Options{Strategy: "ssh", SSHHost: "existing", SSHUser: "user"},
		&artifacts.Resolver{Env: env.Env})
	require.ErrorContains(t, err, "does not consume the image")
	assert.Empty(t, env.CommandsExecuted)
}
