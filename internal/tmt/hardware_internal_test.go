// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//nolint:testpackage,nolintlint // Internal helper tests intentionally use the implementation package.
package tmt

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/testctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlattenHardwareConstraints(t *testing.T) {
	constraints, err := flattenHardwareConstraints("", map[string]any{
		"memory": ">= 16 GB",
		"cpu": map[string]any{
			"cores": ">= 4", "threads": ">=8",
		},
		"disk": []any{map[string]any{"size": ">= 512 GB"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"cpu.cores >= 4", "cpu.threads >=8", "disk[0].size >= 512 GB", "memory >= 16 GB",
	}, constraints)

	for _, test := range []struct {
		name   string
		prefix string
		value  any
		want   []string
		err    string
	}{
		{"number", "cpu.cores", float64(4), []string{"cpu.cores = 4"}, ""},
		{"boolean", "cpu.flag", true, []string{"cpu.flag = true"}, ""},
		{"and group", "", map[string]any{"and": []any{}}, nil, "cannot be safely overridden"},
		{"or group", "", map[string]any{"or": []any{}}, nil, "cannot be safely overridden"},
		{"nested group", "disk", []any{map[string]any{"or": []any{}}}, nil, "cannot be safely overridden"},
		{"no string key", "", "16 GB", nil, "has no key"},
		{"no numeric key", "", float64(2), nil, "has no key"},
		{"unsupported null", "cpu", nil, nil, "unsupported hardware constraint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := flattenHardwareConstraints(test.prefix, test.value)
			if test.err != "" {
				require.ErrorContains(t, err, test.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, actual)
		})
	}
}

func TestMergePlanHardwareForcesUEFI(t *testing.T) {
	actual, err := mergePlanHardwareArgs(map[string]any{
		"boot": map[string]any{"method": "= bios"}, "memory": ">= 8 GB",
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"memory >= 8 GB", "boot.method = uefi"}, actual)

	actual, err = mergePlanHardwareArgs(nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"boot.method = uefi"}, actual)
}

func TestExportedPlanHardware(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   map[string]any
		err    string
	}{
		{
			"matching plan", `[{"name":"/other"},{"name":"/smoke","provision":[{"hardware":{"memory":">= 8 GB"}}]}]`,
			map[string]any{"memory": ">= 8 GB"},
			"",
		},
		{"no hardware", `[{"name":"/smoke","provision":[{}]}]`, map[string]any{}, ""},
		{"no provision", `[{"name":"/smoke"}]`, map[string]any{}, ""},
		{"invalid json", `{`, nil, "decode TMT plan export"},
		{"missing plan", `[{"name":"/other"}]`, nil, "was not returned"},
		{"duplicate plan", `[{"name":"/smoke"},{"name":"/smoke"}]`, nil, "multiple exported plans"},
		{
			"multiple guests", `[{"name":"/smoke","provision":[{"user":"first"},{"user":"second"}]}]`,
			nil, "multiple provision phases",
		},
		{"invalid hardware", `[{"name":"/smoke","provision":[{"hardware":"invalid"}]}]`, nil, "must be a mapping"},
		{"null hardware", `[{"name":"/smoke","provision":[{"hardware":null}]}]`, map[string]any{}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := exportedPlanHardware([]byte(test.output), "/smoke")
			if test.err != "" {
				require.ErrorContains(t, err, test.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.want, actual)
		})
	}
}

func TestResolvedPlanHardwareDryRun(t *testing.T) {
	ctx := testctx.NewCtx()
	ctx.DryRunValue = true
	ctx.CmdFactory.RunAndGetOutputHandler = func(*exec.Cmd) (string, error) {
		t.Fatal("plan export must not execute in dry-run")

		return "", nil
	}
	actual, err := resolvedPlanHardwareArgs(ctx, "/source", "/venv/tmt", "/smoke")
	require.NoError(t, err)
	assert.Equal(t, []string{"boot.method = uefi"}, actual)
}

func TestResolvedPlanVirtualUser(t *testing.T) {
	for _, test := range []struct {
		name, output, user, wantError string
	}{
		{"default user", `[{"name":"/smoke","provision":[{"how":"virtual"}]}]`, "azureuser", ""},
		{"no provision", `[{"name":"/smoke"}]`, "azureuser", ""},
		{"declared user", `[{"name":"/smoke","provision":[{"user":"tester"}]}]`, "tester", ""},
		{"empty user", `[{"name":"/smoke","provision":[{"user":""}]}]`, "", "non-empty string"},
		{"invalid user", `[{"name":"/smoke","provision":[{"user":true}]}]`, "", "non-empty string"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := testctx.NewCtx()
			ctx.CmdFactory.RunAndGetOutputHandler = func(cmd *exec.Cmd) (string, error) {
				assert.Equal(t, []string{
					"/venv/tmt", "-c", "distro=azurelinux-4", "plan", "export", "^/smoke$", "--how", "json",
				}, cmd.Args)

				return test.output, nil
			}

			_, user, err := resolvedPlanSettings(ctx, "/source", "/venv/tmt", "/smoke", "-c", "distro=azurelinux-4")
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, test.user, user)
		})
	}
}

func TestPlanSelectorIsExactAndLiteral(t *testing.T) {
	name := "/plans/smoke.v1+[host]"
	pattern, err := regexp.Compile(exactPlanName(name))
	require.NoError(t, err)
	assert.True(t, pattern.MatchString(name))

	for _, other := range []string{name + "/extra", "/prefix" + name, "/plans/smokeXv11h"} {
		assert.False(t, pattern.MatchString(other), other)
	}
}

func TestCommandOutputRetainsStderr(t *testing.T) {
	ctx := testctx.NewCtx()
	expected := errors.New("native failure")
	ctx.CmdFactory.RunAndGetOutputHandler = func(command *exec.Cmd) (string, error) {
		assert.Equal(t, "/source", command.Dir)
		_, err := command.Stderr.Write([]byte("export diagnostic"))
		require.NoError(t, err)

		return "", expected
	}
	_, err := commandOutput(ctx, "/source", "/venv/tmt", "plan", "export")
	require.ErrorIs(t, err, expected)
	require.ErrorContains(t, err, "export diagnostic")
}

func TestCommandEnv(t *testing.T) {
	for _, test := range []struct {
		name     string
		strategy testframework.Strategy
		plugin   string
		timeout  string
		want     []string
	}{
		{"host acknowledgement", testframework.Host, "", "", []string{"TMT_BOOT_TIMEOUT=300", "TMT_FEELING_SAFE=1"}},
		{"VM plugin", testframework.VM, "/plugins", "", []string{"TMT_PLUGINS=/plugins", "TMT_BOOT_TIMEOUT=300"}},
		{"timeout preserved", testframework.Host, "", "900", []string{"TMT_FEELING_SAFE=1"}},
		{"SSH no plugin", testframework.SSH, "", "", []string{"TMT_BOOT_TIMEOUT=300"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := testctx.NewTestOSEnv()
			env.SetEnv("TMT_BOOT_TIMEOUT", test.timeout)
			ctx := testctx.NewCtx(testctx.WithOSEnv(env))
			assert.Equal(t, test.want, commandEnv(ctx, test.plugin, test.strategy))
		})
	}
}

func TestTestcloudPluginUsesOnlyConfiguredBootstrapAndScopedPull(t *testing.T) {
	for _, commands := range [][]string{nil, {`printf '%s\n' "quoted"`, "echo second"}} {
		ctx := testctx.NewCtx()
		pluginDir, err := writeTestcloudPlugin(ctx, "/work/run", commands...)
		require.NoError(t, err)
		plugin, err := fileutils.ReadFile(ctx.FS(), filepath.Join(pluginDir, "azldev_testcloud.py"))
		require.NoError(t, err)

		text := string(plugin)
		_, encoded, found := strings.Cut(text, "provision.TESTCLOUD_WORKAROUNDS.extend(")
		require.True(t, found)
		encoded, _, found = strings.Cut(encoded, ")\n")
		require.True(t, found)

		var decoded []string
		require.NoError(t, json.Unmarshal([]byte(encoded), &decoded))
		assert.Equal(t, append([]string{}, commands...), decoded)
		assert.Contains(t, text, `config["ssh_pwauth"] = False`)
		assert.Contains(t, text, `YAML(typ="safe")`)
		assert.Contains(t, text, "yaml.width = sys.maxsize")
		assert.Contains(t, text, "provision.GuestTestcloud.pull = _azldev_pull")
		assert.Contains(t, text, `"--rsync-path", "sudo -n rsync"`)
		assert.NotContains(t, text, "GuestSsh.pull =")
		assert.NotContains(t, text, "USER_DATA")
		assert.NotContains(t, text, "firewall-cmd")
		assert.NotContains(t, text, "fedora")
		assert.NotContains(t, text, "chmod")
		assert.NotContains(t, text, "default_ssh_master_start_waiting")
	}
}

func TestDirsOverlap(t *testing.T) {
	for _, test := range []struct {
		first, second string
		want          bool
	}{
		{"/src", "/src", true},
		{"/src", "/src/child", true},
		{"/src/child", "/src", true},
		{"/src", "/src-other", false},
		{"/src", "/work", false},
		{"/src/a/..", "/src", true},
	} {
		assert.Equal(t, test.want, dirsOverlap(test.first, test.second))
	}
}
