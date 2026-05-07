// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePluginWithProviders constructs a Plugin pre-populated with the
// given tools and a manifest declaring providers binding each named
// provider to a tool. Used by registry tests to keep the table-driven
// cases concise.
func makePluginWithProviders(name, path string, tools []mcp.Tool, providers []ProviderRef) *Plugin {
	plugin := makePlugin(name, path, "1.0.0", tools)
	plugin.manifest = &Manifest{
		ProtocolVersion: 1,
		Providers:       providers,
	}

	return plugin
}

func TestBuildRegistry_Empty(t *testing.T) {
	t.Parallel()

	registry, err := BuildRegistry(nil)
	require.NoError(t, err)
	require.NotNil(t, registry)
	assert.Empty(t, registry.All())
	assert.Empty(t, registry.Kinds())
}

func TestBuildRegistry_NoManifest(t *testing.T) {
	t.Parallel()

	plugin := makePlugin("p", "/dev/null/p", "1.0.0",
		[]mcp.Tool{makeTool("t", nil, nil)})

	registry, err := BuildRegistry([]*Plugin{plugin})
	require.NoError(t, err)
	assert.Empty(t, registry.All(),
		"plugins without a manifest contribute no providers")
}

func TestBuildRegistry_RegistersProviders(t *testing.T) {
	t.Parallel()

	build := makeTool("cloud-build", nil, nil)
	test := makeTool("cloud-test", nil, nil)

	plugin := makePluginWithProviders("p", "/dev/null/p",
		[]mcp.Tool{build, test},
		[]ProviderRef{
			{Kind: "builder", Name: "cloud", Tool: "cloud-build"},
			{Kind: "tester", Name: "cloud", Tool: "cloud-test"},
		})

	registry, err := BuildRegistry([]*Plugin{plugin})
	require.NoError(t, err)

	got, err := registry.Lookup("builder", "cloud")
	require.NoError(t, err)
	assert.Equal(t, "cloud-build", got.Tool.Name)
	assert.Same(t, plugin, got.Plugin)

	tester, err := registry.Lookup("tester", "cloud")
	require.NoError(t, err)
	assert.Equal(t, "cloud-test", tester.Tool.Name)

	assert.ElementsMatch(t, []string{"builder", "tester"}, registry.Kinds())
}

func TestBuildRegistry_RejectsCollision(t *testing.T) {
	t.Parallel()

	tool := makeTool("build", nil, nil)
	plugA := makePluginWithProviders("a", "/dev/null/a",
		[]mcp.Tool{tool}, []ProviderRef{{Kind: "builder", Name: "cloud", Tool: "build"}})
	plugB := makePluginWithProviders("b", "/dev/null/b",
		[]mcp.Tool{tool}, []ProviderRef{{Kind: "builder", Name: "cloud", Tool: "build"}})

	_, err := BuildRegistry([]*Plugin{plugA, plugB})
	require.ErrorIs(t, err, ErrProviderCollision)
}

func TestBuildRegistry_RejectsMissingTool(t *testing.T) {
	t.Parallel()

	plugin := makePluginWithProviders("p", "/dev/null/p",
		[]mcp.Tool{makeTool("real-tool", nil, nil)},
		[]ProviderRef{{Kind: "builder", Name: "cloud", Tool: "ghost-tool"}})

	_, err := BuildRegistry([]*Plugin{plugin})
	require.ErrorIs(t, err, ErrProviderToolMissing)
}

func TestRegistry_Lookup_ReportsAvailableNames(t *testing.T) {
	t.Parallel()

	plugin := makePluginWithProviders("p", "/dev/null/p",
		[]mcp.Tool{
			makeTool("a", nil, nil),
			makeTool("b", nil, nil),
		},
		[]ProviderRef{
			{Kind: "builder", Name: "alpha", Tool: "a"},
			{Kind: "builder", Name: "beta", Tool: "b"},
		})

	registry, err := BuildRegistry([]*Plugin{plugin})
	require.NoError(t, err)

	_, err = registry.Lookup("builder", "ghost")
	require.ErrorIs(t, err, ErrProviderNotFound)
	assert.Contains(t, err.Error(), "alpha")
	assert.Contains(t, err.Error(), "beta",
		"error should list available names so users can correct their input")
}

func TestRegistry_ListByKind_SortedByName(t *testing.T) {
	t.Parallel()

	plugin := makePluginWithProviders("p", "/dev/null/p",
		[]mcp.Tool{
			makeTool("a", nil, nil),
			makeTool("b", nil, nil),
			makeTool("c", nil, nil),
		},
		[]ProviderRef{
			{Kind: "builder", Name: "charlie", Tool: "c"},
			{Kind: "builder", Name: "alpha", Tool: "a"},
			{Kind: "builder", Name: "bravo", Tool: "b"},
		})

	registry, err := BuildRegistry([]*Plugin{plugin})
	require.NoError(t, err)

	got := registry.ListByKind("builder")
	require.Len(t, got, 3)
	assert.Equal(t, "alpha", got[0].Ref.Name)
	assert.Equal(t, "bravo", got[1].Ref.Name)
	assert.Equal(t, "charlie", got[2].Ref.Name)
}

func TestParseManifest_PopulatesProviders(t *testing.T) {
	t.Parallel()

	body := `{
		"protocol-version": 1,
		"providers": [
			{ "kind": "builder",        "name": "cloud", "tool": "cloud-build" },
			{ "kind": "source-provider", "name": "cdn",   "tool": "cdn-fetch"   }
		]
	}`

	var manifest Manifest
	require.NoError(t, json.Unmarshal([]byte(body), &manifest))

	require.Len(t, manifest.Providers, 2)
	assert.Equal(t, "builder", manifest.Providers[0].Kind)
	assert.Equal(t, "cloud", manifest.Providers[0].Name)
	assert.Equal(t, "cloud-build", manifest.Providers[0].Tool)
	assert.Equal(t, "source-provider", manifest.Providers[1].Kind)
}
