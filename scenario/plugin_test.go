// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build scenario

package scenario_tests

import (
	"strings"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/cmdtest"
	"github.com/microsoft/azure-linux-dev-tools/scenario/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pluginTestSetup locates the reference plugin binary built by 'mage build'.
// All plugin scenario tests share this lookup; it skips the test gracefully
// when the binary isn't on disk so the suite can still run on partial builds.
func pluginTestSetup(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping long test")
	}

	binPath, err := testhelpers.FindAuxBinary("azldev-plugin-hello")
	require.NoError(t, err, "the reference plugin binary must be built before running plugin scenario tests")

	return binPath
}

// TestPlugin_HelpListsLoadedPlugin confirms that loading a plugin via the
// '--plugin' flag adds the 'plugin' command group to root '--help'.
func TestPlugin_HelpListsLoadedPlugin(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest("--plugin", pluginBin, "--help", "--color=never").
		Locally().
		Run(t)
	require.NoError(t, err)
	require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)

	combined := results.Stdout + results.Stderr
	assert.Contains(t, combined, "plugin",
		"top-level help should mention the 'plugin' command")
	assert.Contains(t, combined, "Plugin commands:",
		"plugin group title should appear in --help")
}

// TestPlugin_HelpShowsPluginAndTool walks down the dynamically-registered
// command tree to verify the plugin name, tool name and required flags all
// surface through Cobra '--help'.
func TestPlugin_HelpShowsPluginAndTool(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	t.Run("plugin namespace", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin, "plugin", "--help", "--color=never").
			Locally().
			Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.Contains(t, results.Stdout, "hello",
			"per-plugin help should list the 'hello' subcommand")
	})

	t.Run("plugin per-tool subcommand", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin, "plugin", "hello", "--help", "--color=never").
			Locally().
			Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.Contains(t, results.Stdout, "greet",
			"per-plugin help should list the 'greet' tool")
	})

	t.Run("tool flag help", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin, "plugin", "hello", "greet", "--help", "--color=never").
			Locally().
			Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.Contains(t, results.Stdout, "--name",
			"tool help should advertise the --name flag")
		assert.Contains(t, results.Stdout, "--salutation",
			"tool help should advertise the --salutation flag")
	})
}

// TestPlugin_InvokeRoundTrip exercises the full Cobra-flag -> MCP CallTool
// -> textual result roundtrip with the reference plugin.
func TestPlugin_InvokeRoundTrip(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest(
		"--plugin", pluginBin,
		"plugin", "hello", "greet",
		"--name=world",
		"--salutation=howdy",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
	assert.Equal(t, "howdy, world!\n", results.Stdout)
}

// TestPlugin_InvokeUsesPluginDefaults confirms that flags left at their
// Cobra default are *omitted* from the MCP call so the plugin's own
// schema-declared default takes effect (here: salutation defaults to
// "hello").
func TestPlugin_InvokeUsesPluginDefaults(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest(
		"--plugin", pluginBin,
		"plugin", "hello", "greet",
		"--name=world",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
	assert.Equal(t, "hello, world!\n", results.Stdout)
}

// TestPlugin_RequiredFlagEnforced verifies that Cobra rejects invocations
// that leave a JSON-Schema-required flag unset.
func TestPlugin_RequiredFlagEnforced(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest(
		"--plugin", pluginBin,
		"plugin", "hello", "greet",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.NotZero(t, results.ExitCode,
		"missing a required flag must produce a non-zero exit code")
	assert.Contains(t, results.Stderr+results.Stdout, "name",
		"error output should mention the missing required flag")
}

// TestPlugin_BadPathFailsFast confirms that pointing '--plugin' at a
// nonexistent path surfaces a clear, fail-fast error rather than silently
// continuing without that plugin.
func TestPlugin_BadPathFailsFast(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping long test")
	}

	results, err := cmdtest.NewScenarioTest(
		"--plugin", "/definitely/not/a/real/path/azldev-plugin",
		"--help",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.NotZero(t, results.ExitCode,
		"a missing plugin binary must cause a non-zero exit code")
	assert.True(t,
		strings.Contains(results.Stderr, "spawn") || strings.Contains(results.Stderr, "plugin"),
		"error output should mention plugin or spawn failure; got: %s", results.Stderr)
}

// TestPlugin_GraftedToolUnderComponent verifies that the reference
// plugin's 'cloud-greet' tool — which sets '_meta.azldev.command-path' to
// ["component", "cloud-greet"] — appears as a child of the built-in
// 'component' command rather than under the 'plugin <name>' fallback
// namespace, and is invokable at its grafted path.
func TestPlugin_GraftedToolUnderComponent(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	t.Run("appears in component --help", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin, "component", "--help", "--color=never").
			Locally().Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.Contains(t, results.Stdout, "cloud-greet",
			"grafted tool should be listed under 'component'")
	})

	t.Run("invokable at grafted path", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin,
			"component", "cloud-greet",
			"--name=alice", "--salutation=howdy",
			"--color=never",
		).Locally().Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.Equal(t, "howdy, alice!\n", results.Stdout,
			"grafted tool must invoke through the same MCP roundtrip as the namespace fallback")
	})

	t.Run("not duplicated in fallback namespace", func(t *testing.T) {
		t.Parallel()

		results, err := cmdtest.NewScenarioTest(
			"--plugin", pluginBin, "plugin", "hello", "--help", "--color=never").
			Locally().Run(t)
		require.NoError(t, err)
		require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)
		assert.NotContains(t, results.Stdout, "cloud-greet",
			"successfully-grafted tool must not also appear in the fallback namespace")
	})
}

// TestPlugin_AdvancedPluginList exercises the 'azldev advanced plugin
// list' admin command. We use JSON output to avoid coupling the test to
// the table layout.
func TestPlugin_AdvancedPluginList(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest(
		"--plugin", pluginBin,
		"-O", "json",
		"advanced", "plugin", "list",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)

	assert.Contains(t, results.Stdout, `"Name": "hello"`)
	assert.Contains(t, results.Stdout, `"Tools": 2`,
		"reference plugin advertises two tools (greet + cloud-greet)")
	assert.Contains(t, results.Stdout, `"HasManifest": true`,
		"reference plugin publishes a manifest")
	assert.Contains(t, results.Stdout, `"ManifestTitle": "Hello plugin (reference)"`)
}

// TestPlugin_AdvancedPluginInfo exercises the 'azldev advanced plugin
// info <name>' admin command and confirms it surfaces the resolved
// destination for both grafted and namespace-fallback tools.
func TestPlugin_AdvancedPluginInfo(t *testing.T) {
	t.Parallel()

	pluginBin := pluginTestSetup(t)

	results, err := cmdtest.NewScenarioTest(
		"--plugin", pluginBin,
		"-O", "json",
		"advanced", "plugin", "info", "hello",
		"--color=never",
	).Locally().Run(t)
	require.NoError(t, err)
	require.Zero(t, results.ExitCode, "stderr=%s", results.Stderr)

	assert.Contains(t, results.Stdout, `"Destination": "azldev component cloud-greet"`,
		"info should report grafted destination for cloud-greet")
	assert.Contains(t, results.Stdout, `"Destination": "azldev plugin hello greet"`,
		"info should report namespace destination for greet")
	assert.Contains(t, results.Stdout, `"protocol-version": 1`,
		"info should embed the parsed manifest")
}
