// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeRecoveredOptions(t *testing.T) {
	raw := map[string]any{
		"source": map[string]any{
			"git-url": "https://example.test/repo", "ref": "0123456789012345678901234567890123456789",
			"external": map[string]any{"owner": "upstream"},
		},
		"plan": "/plans/smoke",
		"context": map[string]any{
			"distro": []any{"azurelinux-4"}, "arch": []string{"x86_64", "aarch64"},
		},
		"pip-extras":         []string{"report-junit", "provision_virtual", "report-junit"},
		"run-extra-args":     []any{"--show-time"},
		"plan-extra-args":    []string{"--filter", "tag:smoke"},
		"cloud-init-runcmds": []string{"echo configured-hook"},
		"provision": map[string]any{
			"how": "virtual", "extra-args": []any{"--memory", "4096", "--connection=system"},
			"external": true,
		},
		"external": map[string]any{"report": true},
	}
	runner, err := New("smoke", raw)
	require.NoError(t, err)
	assert.Equal(t, "/plans/smoke", runner.config.Plan)
	assert.Equal(t, "https://example.test/repo", runner.config.Source.GitURL)
	assert.Equal(t, "virtual", runner.config.Provision.How)
	assert.Equal(t, []string{"--memory", "4096", "--connection=system"}, runner.config.Provision.ExtraArgs)
	assert.Equal(t, []string{"--filter", "tag:smoke"}, runner.config.PlanExtraArgs)
	assert.Equal(t, []string{"echo configured-hook"}, runner.config.CloudInitRuncmds)
	assert.Equal(t, []string{
		"-vv", "-c", "arch=x86_64", "-c", "arch=aarch64", "-c", "distro=azurelinux-4", "--show-time",
	}, runner.config.globalArgs(true))
	assert.Equal(t, []string{"provision-virtual", "report-junit"}, runner.config.installExtras(testframework.VM))
	assert.Equal(t, []string{"report-junit", "provision_virtual", "report-junit"}, raw["pip-extras"])
	assert.Equal(t, map[string]any{"report": true}, raw["external"])

	context, ok := raw["context"].(map[string]any)
	require.True(t, ok)
	arch, ok := context["arch"].([]string)
	require.True(t, ok)

	arch[0] = "changed-arch"

	assert.Equal(t, []string{"x86_64", "aarch64"}, runner.config.Context["arch"])

	args := runner.config.globalArgs(false)
	args[0] = "changed-argument"

	assert.Equal(t, []string{"--show-time"}, runner.config.RunExtraArgs)
}

func TestOptionDefaultsAndAliases(t *testing.T) {
	runner, err := New("defaults", nil)
	require.NoError(t, err)
	assert.Empty(t, runner.config.globalArgs(false))
	assert.Equal(t, []string{"-vv"}, runner.config.globalArgs(true))
	assert.Equal(t, []string{"provision-virtual"}, runner.config.installExtras(testframework.VM))
	assert.Empty(t, runner.config.installExtras(testframework.Container))

	runner, err = New("legacy", map[string]any{"provision-extra-args": []string{"-m4096"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"-m4096"}, runner.config.Provision.ExtraArgs)

	context := map[string][]string{"distro": {"azurelinux"}}
	runner, err = New("typed", map[string]any{"context": context})
	require.NoError(t, err)

	context["distro"][0] = "changed-distro"

	assert.Equal(t, []string{"-c", "distro=azurelinux"}, runner.config.globalArgs(false))

	for _, verbosity := range []string{"-v", "-vvvvv", "-dv", "--verbose", "-q", "--quiet"} {
		t.Run(verbosity, func(t *testing.T) {
			runner, err := New("verbose", map[string]any{"run-extra-args": []string{verbosity}})
			require.NoError(t, err)
			assert.Equal(t, []string{verbosity}, runner.config.globalArgs(true))
		})
	}
}

func TestRecoveredOptionsRejectMalformedConfig(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"context not table", map[string]any{"context": "arch=x86_64"}, "'tmt.context'"},
		{"context scalar", map[string]any{"context": map[string]any{"arch": "x86_64"}}, "array of strings"},
		{"context nonstring", map[string]any{"context": map[string]any{"arch": []any{true}}}, "must be a string"},
		{"empty context dimension", map[string]any{"context": map[string][]string{"": {"x"}}}, "dimension name"},
		{"space context dimension", map[string]any{"context": map[string][]string{"bad name": {"x"}}}, "dimension name"},
		{"file context dimension", map[string]any{"context": map[string][]string{"@path": {"x"}}}, "dimension name"},
		{"equal context dimension", map[string]any{"context": map[string][]string{"a=b": {"x"}}}, "dimension name"},
		{"empty context array", map[string]any{"context": map[string][]string{"arch": {}}}, "at least one"},
		{"empty context value", map[string]any{"context": map[string][]string{"arch": {""}}}, "empty value"},
		{"null context value", map[string]any{"context": map[string][]string{"arch": {"a\x00b"}}}, "null byte"},
		{"provision not table", map[string]any{"provision": "virtual"}, "must be a table"},
		{"how not string", map[string]any{"provision": map[string]any{"how": 3}}, "must be a string"},
		{"unknown how", map[string]any{"provision": map[string]any{"how": "cloud"}}, "unsupported"},
		{"extra args scalar", map[string]any{"run-extra-args": "--quiet"}, "array of strings"},
		{"null arg", map[string]any{"run-extra-args": []string{"--quiet\x00"}}, "null byte"},
		{"bad pip type", map[string]any{"pip-extras": []any{1}}, "must be a string"},
		{"bad extra syntax", map[string]any{"pip-extras": []string{"foo] @ https://example.test"}}, "invalid extra"},
		{"pip flag injection", map[string]any{"pip-extras": []string{"--index-url"}}, "invalid extra"},
		{"empty extra", map[string]any{"pip-extras": []string{""}}, "invalid extra"},
		{"provision args scalar", map[string]any{"provision": map[string]any{"extra-args": "-m2048"}}, "array"},
		{"ambiguous alias", map[string]any{
			"provision": map[string]any{"extra-args": []string{}}, "provision-extra-args": []string{},
		}, "only one"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, err := New("bad", test.raw)
			require.ErrorContains(t, err, test.want)
			assert.Nil(t, runner)
		})
	}
}

func TestExtraArgsRejectOwnedOptionsAndStepInjection(t *testing.T) {
	owned := []string{
		"--image", "--image=/other", "-i/other", "-i=/other", "-vi/other",
		"--arch=aarch64", "-aaarch64", "-a=aarch64", "--how=local", "-hlocal", "-h=local",
		"--root=/other", "-r/other", "--source=/other", "--workdir-root=/other",
		"--id=/other", "--last", "--all", "--keep", "--remove", "--scratch", "--dry",
		"--since=report", "--until=provision", "--skip=execute", "--name=/other", "-n/other",
		"--hardware=boot.method=bios", "--guest=other", "--user=root", "--key=/other",
		"--file=/other.xml", "--report=junit", "--help", "--version",
		"run", "plan", "provision", "discover", "prepare", "execute", "report", "finish", "cleanup",
		"prov", "rep", "--", "-", "", "--unknown=value",
	}
	for _, scope := range []string{"run-extra-args", "plan-extra-args", "provision-extra-args"} {
		for _, arg := range owned {
			t.Run(scope+"/"+arg, func(t *testing.T) {
				runner, err := New("bad", map[string]any{scope: []string{arg}})
				require.Error(t, err)
				assert.Nil(t, runner)
			})
		}
	}

	for _, args := range [][]string{
		{"--filter"},
		{"--filter", "--name=/other"},
		{"--filter="},
		{"-f="},
		{"--filter", "tag:smoke", "report"},
		{"--quiet=true"},
		{"-q=1"},
	} {
		_, err := New("bad", map[string]any{"plan-extra-args": args})
		require.Error(t, err, "%v", args)
	}
}

func TestExtraArgsAcceptScopedValues(t *testing.T) {
	for _, test := range []struct {
		scope string
		args  []string
	}{
		{"run-extra-args", []string{"-vdq", "--no-color", "--show-time", "--log-topic", "command-events"}},
		{"plan-extra-args", []string{"--filter", "tag:smoke", "--condition=enabled == true", "-x/flaky"}},
		{"plan-extra-args", []string{"-vf=tag:smoke", "--filter", "execute"}},
		{"provision-extra-args", []string{"--memory=4096", "-D20", "-c=system", "--stop-retries", "3"}},
	} {
		t.Run(test.scope, func(t *testing.T) {
			_, err := New("valid", map[string]any{test.scope: test.args})
			require.NoError(t, err)
		})
	}
}

func TestConfiguredProvisionerRequiresMatchingStrategy(t *testing.T) {
	for _, test := range []struct {
		how      string
		strategy testframework.Strategy
	}{
		{"virtual", testframework.VM},
		{"container", testframework.Container},
		{"connect", testframework.SSH},
		{"local", testframework.Host},
	} {
		t.Run(test.how, func(t *testing.T) {
			runner, err := New("test", map[string]any{"provision": map[string]any{"how": test.how}})
			require.NoError(t, err)

			for _, strategy := range []testframework.Strategy{
				testframework.VM, testframework.Container, testframework.SSH, testframework.Host,
			} {
				execution := testframework.ExecutionSpec{Strategy: strategy, SourceDir: "/source", Plan: "/smoke"}
				if strategy == testframework.SSH {
					execution.SSHHost, execution.SSHUser = "machine", "tester"
				}

				_, err := runner.Requirements(execution)
				if strategy == test.strategy {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "'tmt.provision.how'")
				}
			}
		})
	}

	runner, err := New("cloudinit", map[string]any{"cloud-init-runcmds": []string{"echo ready"}})
	require.NoError(t, err)
	_, err = runner.Requirements(testframework.ExecutionSpec{
		Strategy: testframework.Container, SourceDir: "/source",
	})
	require.ErrorContains(t, err, "'tmt.cloud-init-runcmds' requires strategy 'vm'")
}
