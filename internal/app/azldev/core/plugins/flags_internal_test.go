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

// makeTool is a small helper that returns a [mcp.Tool] whose input schema is
// constructed from the supplied parts. It keeps the table-driven cases
// terse and readable.
func makeTool(name string, properties map[string]any, required []string) mcp.Tool {
	return mcp.Tool{
		Name: name,
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: properties,
			Required:   required,
		},
	}
}

func TestAddToolFlags_SupportedTypes(t *testing.T) {
	t.Parallel()

	tool := makeTool("t", map[string]any{
		"name":    map[string]any{"type": "string", "description": "the name"},
		"count":   map[string]any{"type": "integer", "default": float64(3)},
		"ratio":   map[string]any{"type": "number", "default": float64(1.5)},
		"verbose": map[string]any{"type": "boolean", "default": true},
	}, []string{"name"})

	cmd := &cobra.Command{Use: "t"}
	require.NoError(t, addToolFlags(cmd, tool))

	// Each property should have produced a flag of the right type.
	require.NotNil(t, cmd.Flags().Lookup("name"))
	assert.Equal(t, "string", cmd.Flags().Lookup("name").Value.Type())
	assert.Equal(t, "int64", cmd.Flags().Lookup("count").Value.Type())
	assert.Equal(t, "float64", cmd.Flags().Lookup("ratio").Value.Type())
	assert.Equal(t, "bool", cmd.Flags().Lookup("verbose").Value.Type())

	// Defaults are propagated.
	assert.Equal(t, "3", cmd.Flags().Lookup("count").DefValue)
	assert.Equal(t, "1.5", cmd.Flags().Lookup("ratio").DefValue)
	assert.Equal(t, "true", cmd.Flags().Lookup("verbose").DefValue)

	// Required is enforced via cobra's annotation.
	annotations := cmd.Flags().Lookup("name").Annotations
	assert.Contains(t, annotations, cobra.BashCompOneRequiredFlag)
}

func TestAddToolFlags_UnsupportedShapes(t *testing.T) {
	t.Parallel()

	cases := map[string]map[string]any{
		"object property": {
			"obj": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		"array property": {
			"arr": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		},
		"missing type": {
			"x": map[string]any{"description": "no type"},
		},
		"oneOf keyword": {
			"x": map[string]any{
				"type":  "string",
				"oneOf": []any{},
			},
		},
		"multi-type": {
			"x": map[string]any{"type": []any{"string", "null"}},
		},
		"unknown type": {
			"x": map[string]any{"type": "null"},
		},
	}

	for name, props := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := &cobra.Command{Use: "t"}
			err := addToolFlags(cmd, makeTool("t", props, nil))
			require.Error(t, err)
			require.ErrorIs(t, err, ErrUnsupportedSchema)

			// On error the command should be left untouched; no flags
			// registered.
			assert.Equal(t, 0, cmd.Flags().NFlag())
		})
	}
}

func TestCollectFlagValues_OnlyChangedFlags(t *testing.T) {
	t.Parallel()

	tool := makeTool("t", map[string]any{
		"name":  map[string]any{"type": "string"},
		"count": map[string]any{"type": "integer", "default": float64(7)},
	}, []string{"name"})

	cmd := &cobra.Command{
		Use:  "t",
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	require.NoError(t, addToolFlags(cmd, tool))

	// Only --name is set; --count is left at its (schema) default. The
	// collector must omit --count entirely so the plugin sees its own
	// default rather than the cobra-side default.
	cmd.SetArgs([]string{"--name=alice"})
	require.NoError(t, cmd.Execute())

	args, err := collectFlagValues(cmd, tool)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"name": "alice"}, args)
}

func TestCollectFlagValues_TypedRoundTrip(t *testing.T) {
	t.Parallel()

	tool := makeTool("t", map[string]any{
		"name":    map[string]any{"type": "string"},
		"count":   map[string]any{"type": "integer"},
		"ratio":   map[string]any{"type": "number"},
		"verbose": map[string]any{"type": "boolean"},
	}, nil)

	cmd := &cobra.Command{
		Use:  "t",
		RunE: func(_ *cobra.Command, _ []string) error { return nil },
	}
	require.NoError(t, addToolFlags(cmd, tool))

	cmd.SetArgs([]string{
		"--name=alice",
		"--count=42",
		"--ratio=2.75",
		"--verbose=true",
	})
	require.NoError(t, cmd.Execute())

	args, err := collectFlagValues(cmd, tool)
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"name":    "alice",
		"count":   int64(42),
		"ratio":   2.75,
		"verbose": true,
	}, args)
}

func TestCollectFlagValues_UnsupportedSchema(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "t"}
	tool := makeTool("t", map[string]any{
		"x": map[string]any{"type": "object", "properties": map[string]any{}},
	}, nil)

	_, err := collectFlagValues(cmd, tool)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedSchema)
}

func TestParseToolProperties_RejectsNonObjectSchema(t *testing.T) {
	t.Parallel()

	tool := mcp.Tool{
		Name: "t",
		InputSchema: mcp.ToolInputSchema{
			Type: "string",
		},
	}

	_, err := parseToolProperties(tool)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsupportedSchema)
}
