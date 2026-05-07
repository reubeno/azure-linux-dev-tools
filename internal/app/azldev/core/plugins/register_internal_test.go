// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"bytes"
	"io"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHost is a small [Host] implementation used by tests; it captures
// output and any registered fix suggestions for assertion.
type fakeHost struct {
	output      bytes.Buffer
	suggestions []string
}

func (h *fakeHost) OutputWriter() io.Writer { return &h.output }
func (h *fakeHost) AddFixSuggestion(s string) {
	h.suggestions = append(h.suggestions, s)
}

// makePlugin synthesizes a [Plugin] with a pre-populated tool catalog for
// tests. The returned plugin has no live MCP client; tool invocations
// against it would fail. Tests that don't invoke tools may construct
// plugins directly using this helper.
func makePlugin(name, path, version string, tools []mcp.Tool) *Plugin {
	return &Plugin{
		path:    path,
		name:    name,
		version: version,
		tools:   tools,
	}
}

func TestRegister_NoPlugins(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}

	require.NoError(t, Register(root, &fakeHost{}, nil))

	// No plugin group should be added when the slice is empty.
	for _, g := range root.Groups() {
		assert.NotEqual(t, CommandGroupID, g.ID,
			"plugin group should not be added when no plugins were loaded")
	}

	for _, c := range root.Commands() {
		assert.NotEqual(t, "plugin", c.Name(),
			"plugin command should not be added when no plugins were loaded")
	}
}

func TestRegister_RegistersToolNamespace(t *testing.T) {
	t.Parallel()

	tool := makeTool("greet", map[string]any{
		"name": map[string]any{"type": "string", "description": "the name"},
	}, []string{"name"})
	tool.Description = "Greet someone by name."

	plugin := makePlugin("hello", "/dev/null/hello", "1.0.0", []mcp.Tool{tool})

	root := &cobra.Command{Use: "azldev"}
	require.NoError(t, Register(root, &fakeHost{}, []*Plugin{plugin}))

	// 'plugin' parent must exist with the right group.
	pluginCmd, _, err := root.Find([]string{"plugin"})
	require.NoError(t, err)
	require.Equal(t, "plugin", pluginCmd.Name())
	assert.Equal(t, CommandGroupID, pluginCmd.GroupID)

	// 'plugin hello' subcommand named after the plugin.
	helloCmd, _, err := root.Find([]string{"plugin", "hello"})
	require.NoError(t, err)
	require.Equal(t, "hello", helloCmd.Name())
	assert.Contains(t, helloCmd.Short, "1.0.0",
		"per-plugin Short should mention the plugin version")

	// 'plugin hello greet' tool leaf with the expected flag.
	greetCmd, _, err := root.Find([]string{"plugin", "hello", "greet"})
	require.NoError(t, err)
	require.Equal(t, "greet", greetCmd.Name())
	assert.Equal(t, "Greet someone by name.", greetCmd.Short)
	assert.NotNil(t, greetCmd.Flags().Lookup("name"))
}

func TestRegister_SkipsToolsWithUnsupportedSchemas(t *testing.T) {
	t.Parallel()

	good := makeTool("good", map[string]any{
		"x": map[string]any{"type": "string"},
	}, nil)

	bad := makeTool("bad", map[string]any{
		"x": map[string]any{"type": "object", "properties": map[string]any{}},
	}, nil)

	plugin := makePlugin("p", "/dev/null/p", "", []mcp.Tool{good, bad})

	root := &cobra.Command{Use: "azldev"}
	require.NoError(t, Register(root, &fakeHost{}, []*Plugin{plugin}))

	// The supported tool registers; the unsupported one is skipped silently
	// so other tools from the same plugin remain usable.
	_, _, err := root.Find([]string{"plugin", "p", "good"})
	require.NoError(t, err)

	pluginGood, _, err := root.Find([]string{"plugin", "p"})
	require.NoError(t, err)

	for _, sub := range pluginGood.Commands() {
		assert.NotEqual(t, "bad", sub.Name(),
			"unsupported tool should be skipped during registration")
	}
}

func TestRegister_RejectsCollidingPluginNames(t *testing.T) {
	t.Parallel()

	a := makePlugin("dup", "/path/a", "", nil)
	b := makePlugin("dup", "/path/b", "", nil)

	root := &cobra.Command{Use: "azldev"}
	err := Register(root, &fakeHost{}, []*Plugin{a, b})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name collision")
}

func TestRegister_RejectsBuiltinPluginCommandCollision(t *testing.T) {
	t.Parallel()

	root := &cobra.Command{Use: "azldev"}
	root.AddCommand(&cobra.Command{Use: "plugin"})

	plugin := makePlugin("hello", "/dev/null/hello", "", nil)
	err := Register(root, &fakeHost{}, []*Plugin{plugin})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestFirstLineOf(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		in, want string
	}{
		"empty":       {"", ""},
		"single line": {"hello", "hello"},
		"multi line":  {"first\nsecond", "first"},
		"leading nl":  {"\nrest", ""},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, firstLineOf(tc.in))
		})
	}
}
