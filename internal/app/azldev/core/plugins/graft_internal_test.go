// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeToolWithMeta constructs an mcp.Tool with the given properties and an
// '_meta.azldev' object populated from azldevMeta. Used by graft tests to
// declare command-path and related hints concisely.
func makeToolWithMeta(name string, properties map[string]any, required []string, azldevMeta map[string]any) mcp.Tool {
	tool := makeTool(name, properties, required)
	tool.Meta = mcp.NewMetaFromMap(map[string]any{MetaKey: azldevMeta})

	return tool
}

func TestParseToolGraftHints_Absent(t *testing.T) {
	t.Parallel()

	hints, err := parseToolGraftHints(makeTool("t", nil, nil))
	require.NoError(t, err)
	assert.Nil(t, hints, "no _meta means no hints")

	tool := makeTool("t", nil, nil)
	tool.Meta = mcp.NewMetaFromMap(map[string]any{"vendor": "other"})

	hints, err = parseToolGraftHints(tool)
	require.NoError(t, err)
	assert.Nil(t, hints, "_meta without azldev key means no hints")
}

func TestParseToolGraftHints_FullShape(t *testing.T) {
	t.Parallel()

	tool := makeToolWithMeta("t", nil, nil, map[string]any{
		"command-path": []any{"component", "cloud-build"},
		"aliases":      []any{"cb"},
		"hidden":       true,
		"group-title":  "Cloud build commands",
	})

	hints, err := parseToolGraftHints(tool)
	require.NoError(t, err)
	require.NotNil(t, hints)
	assert.Equal(t, []string{"component", "cloud-build"}, hints.CommandPath)
	assert.Equal(t, []string{"cb"}, hints.Aliases)
	assert.True(t, hints.Hidden)
	assert.Equal(t, "Cloud build commands", hints.GroupTitle)
}

func TestParseToolGraftHints_Malformed(t *testing.T) {
	t.Parallel()

	// command-path containing a non-string element fails the json
	// unmarshal into []string.
	tool := makeToolWithMeta("t", nil, nil, map[string]any{
		"command-path": []any{"component", 7},
	})

	_, err := parseToolGraftHints(tool)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrManifestMalformed)
}

func TestTryGraft_AttachesToExistingGroup(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}
	root.AddCommand(&cobra.Command{Use: "component", Short: "Manage components"})

	plugin := makePlugin("cloudbuild", "/dev/null/cloudbuild", "1.0.0", nil)
	leaf := &cobra.Command{Use: "placeholder"}
	hints := &ToolGraftHints{CommandPath: []string{"component", "cloud-build"}}

	require.NoError(t, tryGraft(root, leaf, plugin, hints))

	got, _, err := root.Find([]string{"component", "cloud-build"})
	require.NoError(t, err)
	assert.Equal(t, "cloud-build", got.Name())
}

func TestTryGraft_CreatesNewTopLevelGroup(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}

	plugin := makePlugin("p", "/dev/null/p", "", nil)
	leaf := &cobra.Command{Use: "placeholder"}
	hints := &ToolGraftHints{
		CommandPath: []string{"mygroup", "tool"},
		GroupTitle:  "My custom group",
	}

	require.NoError(t, tryGraft(root, leaf, plugin, hints))

	groupCmd, _, err := root.Find([]string{"mygroup"})
	require.NoError(t, err)
	assert.Equal(t, "mygroup", groupCmd.Name())
	assert.Equal(t, "My custom group", groupCmd.Short,
		"group-title should propagate to the deepest newly-created group")
	assert.Equal(t, plugin.Name(),
		groupCmd.Annotations[AnnotationPluginIntroducedBy],
		"plugin-introduced groups should record their owner")
	assert.Equal(t, CommandGroupID, groupCmd.GroupID,
		"new top-level plugin groups should join the plugin command group")

	leafCmd, _, err := root.Find([]string{"mygroup", "tool"})
	require.NoError(t, err)
	assert.Equal(t, "tool", leafCmd.Name())
}

func TestTryGraft_RejectsReservedTopLevel(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}

	plugin := makePlugin("p", "/dev/null/p", "", nil)
	leaf := &cobra.Command{Use: "placeholder"}

	for _, name := range []string{"help", "completion", "version", "advanced", "plugin"} {
		hints := &ToolGraftHints{CommandPath: []string{name, "x"}}
		err := tryGraft(root, leaf, plugin, hints)
		require.ErrorIs(t, err, errGraftPathReserved,
			"reserved root %q must be rejected by tryGraft", name)
	}

	// Tree should be untouched after all the failed attempts.
	assert.Empty(t, root.Commands(), "tryGraft must not mutate root on failure")
}

func TestTryGraft_RejectsBlockedIntermediateLeaf(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}
	// 'component build' is a leaf (has RunE). Plugin trying to put a
	// command beneath it must be rejected.
	component := &cobra.Command{Use: "component"}
	component.AddCommand(&cobra.Command{
		Use:  "build",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(component)

	plugin := makePlugin("p", "/dev/null/p", "", nil)
	leaf := &cobra.Command{Use: "placeholder"}
	hints := &ToolGraftHints{CommandPath: []string{"component", "build", "extra"}}

	err := tryGraft(root, leaf, plugin, hints)
	require.ErrorIs(t, err, errGraftPathBlocked)
}

func TestTryGraft_RejectsLeafCollision(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}
	component := &cobra.Command{Use: "component"}
	component.AddCommand(&cobra.Command{
		Use:  "build",
		RunE: func(*cobra.Command, []string) error { return nil },
	})
	root.AddCommand(component)

	plugin := makePlugin("p", "/dev/null/p", "", nil)
	leaf := &cobra.Command{Use: "placeholder"}
	hints := &ToolGraftHints{CommandPath: []string{"component", "build"}}

	err := tryGraft(root, leaf, plugin, hints)
	require.ErrorIs(t, err, errGraftLeafCollision)

	// 'build' must still be the original built-in (no replacement).
	got, _, err := root.Find([]string{"component", "build"})
	require.NoError(t, err)
	assert.NotNil(t, got.RunE, "built-in command must remain unchanged after rejection")
}

func TestRegister_GraftsTool(t *testing.T) {
	t.Parallel()

	tool := makeToolWithMeta("greet",
		map[string]any{"name": map[string]any{"type": "string"}},
		[]string{"name"},
		map[string]any{"command-path": []any{"component", "cloud-greet"}},
	)
	tool.Description = "Greet someone via the cloud."

	root := &cobra.Command{Use: "azldev"}
	root.AddCommand(&cobra.Command{Use: "component", Short: "Manage components"})

	plugin := makePlugin("cloud", "/dev/null/cloud", "1.0.0", []mcp.Tool{tool})
	require.NoError(t, Register(root, &fakeHost{}, []*Plugin{plugin}))

	// Tool must be reachable at its grafted path.
	got, _, err := root.Find([]string{"component", "cloud-greet"})
	require.NoError(t, err)
	assert.Equal(t, "cloud-greet", got.Name())
	assert.Equal(t, "Greet someone via the cloud.", got.Short)
	require.NotNil(t, got.Flags().Lookup("name"))

	// And NOT under the namespace, since it grafted successfully.
	_, _, err = root.Find([]string{"plugin"})
	require.Error(t, err, "no namespace parent should be installed when all tools graft")
}

func TestRegister_FallsBackOnGraftFailure(t *testing.T) {
	t.Parallel()

	// Tool requests a graft path that targets a built-in leaf — must fall
	// back to the namespace.
	tool := makeToolWithMeta("greet",
		map[string]any{"name": map[string]any{"type": "string"}},
		[]string{"name"},
		map[string]any{"command-path": []any{"help"}}, // reserved root
	)

	root := &cobra.Command{Use: "azldev"}
	plugin := makePlugin("hello", "/dev/null/hello", "1.0.0", []mcp.Tool{tool})

	require.NoError(t, Register(root, &fakeHost{}, []*Plugin{plugin}))

	got, _, err := root.Find([]string{"plugin", "hello", "greet"})
	require.NoError(t, err, "graft-rejected tool must be reachable in the namespace fallback")
	assert.Equal(t, "greet", got.Name())
}

func TestRegister_MixedGraftAndFallback(t *testing.T) {
	t.Parallel()

	grafted := makeToolWithMeta("a-tool",
		map[string]any{"x": map[string]any{"type": "string"}}, nil,
		map[string]any{"command-path": []any{"component", "a-tool"}})

	fallback := makeTool("b-tool",
		map[string]any{"x": map[string]any{"type": "string"}}, nil)

	root := &cobra.Command{Use: "azldev"}
	root.AddCommand(&cobra.Command{Use: "component"})

	plugin := makePlugin("p", "/dev/null/p", "1.0.0", []mcp.Tool{grafted, fallback})
	require.NoError(t, Register(root, &fakeHost{}, []*Plugin{plugin}))

	_, _, err := root.Find([]string{"component", "a-tool"})
	require.NoError(t, err, "grafted tool should land at requested path")

	_, _, err = root.Find([]string{"plugin", "p", "b-tool"})
	require.NoError(t, err, "fallback tool should land in the namespace")
}
