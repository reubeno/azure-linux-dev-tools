// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build scenario

package scenario_tests

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/cmdtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedTestCatalogAndExplicitRun(t *testing.T) {
	t.Parallel()

	config := []string{
		"[project]",
		`default-distro = { name = "azl", version = "4" }`,
		`work-dir = "work"`,
		`output-dir = "out"`,
		`log-dir = "logs"`,
		`[distros.azl.versions."4"]`,
		`release-ver = "4"`,
		"[components.pkg.tests]",
		`tests = [{ name = "smoke" }]`,
		"[images.base.tests]",
		`tests = [{ name = "smoke" }]`,
		"[images.base.test-defaults.tmt]",
		`cloud-init-runcmds = ["echo configured-image-bootstrap"]`,
		"[tests.smoke]",
		`type = "tmt"`,
		"[tests.smoke.tmt]",
		`source = { git-url = "https://example.invalid/tests.git", ref = "0000000000000000000000000000000000000000" }`,
		`plan = "/plans/smoke"`,
	}

	for _, testcase := range []struct {
		name string
		args []string
		run  bool
	}{
		{"catalog", []string{"test", "list"}, false},
		{"component", []string{"test", "list", "--component", "pkg"}, false},
		{"image", []string{"test", "list", "--image", "base"}, false},
		{"run-component", []string{"test", "run", "--component", "pkg", "--rpm", "pkg.rpm",
			"--base-image", "base", "--base-image-path", "base.qcow2", "--dry-run"}, true},
		{"run-image", []string{"test", "run", "--image", "base", "--image-path", "base.qcow2", "--dry-run"}, true},
		{"legacy-component", []string{"component", "test", "pkg", "--rpm", "pkg.rpm",
			"--base-image-path", "base.qcow2", "--dry-run"}, true},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			t.Parallel()

			args := append([]string{"--no-default-config", "-O", "json"}, testcase.args...)
			test := cmdtest.NewScenarioTest(args...).
				AddFileContents("azldev.toml", strings.NewReader(strings.Join(config, "\n")+"\n")).
				AddFileContents("pkg.rpm", strings.NewReader("candidate")).
				AddFileContents("base.qcow2", strings.NewReader("image")).
				InContainer()
			results, err := test.Run(t)
			require.NoError(t, err)
			require.Zerof(t, results.ExitCode, "%s\n%s", results.Stdout, results.Stderr)
			var rows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(results.Stdout), &rows))
			require.Len(t, rows, 1)
			assert.Equal(t, "smoke", rows[0]["name"])
			assert.Equal(t, "tmt", rows[0]["framework"])
			if testcase.run {
				assert.Equal(t, "planned", rows[0]["status"])
				if testcase.name != "legacy-component" {
					assert.Contains(t, results.Stderr, "project-image:base")
				}
			} else {
				assert.NotContains(t, rows[0], "status")
			}
		})
	}
}
