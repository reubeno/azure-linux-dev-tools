// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package config_test

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/azure-linux-dev-tools/internal/app/azldev/cmds/config"
	jsonschemavalidator "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateAzlDevTOMLJSONSchema(t *testing.T) {
	// Generate the schema.
	schemaText, err := config.GenerateAzlDevTOMLJSONSchema()
	require.NoError(t, err)
	require.NotEmpty(t, schemaText)

	deserialized := make(map[string]any)

	// Make sure the schema is valid JSON.
	err = json.Unmarshal([]byte(schemaText), &deserialized)
	require.NoError(t, err)
}

func TestReleaseCounterJSONSchemaValidation(t *testing.T) {
	schemaText, err := config.GenerateAzlDevTOMLJSONSchema()
	require.NoError(t, err)

	var schemaDocument any
	require.NoError(t, json.Unmarshal([]byte(schemaText), &schemaDocument))

	compiler := jsonschemavalidator.NewCompiler()
	require.NoError(t, compiler.AddResource("azldev.schema.json", schemaDocument))

	schema, err := compiler.Compile("azldev.schema.json")
	require.NoError(t, err)

	testCases := []struct {
		name    string
		release map[string]any
		valid   bool
	}{
		{
			name: "release tag",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source": "release-tag",
					"regex":  `^([0-9]+)$`,
				},
			},
			valid: true,
		},
		{
			name: "spec macro",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source":    "spec-macro",
					"directive": "define",
					"name":      "baserelease",
				},
			},
			valid: true,
		},
		{
			name: "release tag missing regex",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source": "release-tag",
				},
			},
		},
		{
			name: "release tag empty regex",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source": "release-tag",
					"regex":  "",
				},
			},
		},
		{
			name: "release tag with macro fields",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source":    "release-tag",
					"regex":     `^([0-9]+)$`,
					"directive": "global",
					"name":      "baserelease",
				},
			},
		},
		{
			name: "spec macro missing name",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source":    "spec-macro",
					"directive": "global",
				},
			},
		},
		{
			name: "spec macro invalid name",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source":    "spec-macro",
					"directive": "global",
					"name":      "base-release",
				},
			},
		},
		{
			name: "spec macro name starts with digit",
			release: map[string]any{
				"calculation": "static",
				"counter": map[string]any{
					"source":    "spec-macro",
					"directive": "global",
					"name":      "1release",
				},
			},
		},
		{
			name: "manual with counter",
			release: map[string]any{
				"calculation": "manual",
				"counter": map[string]any{
					"source": "release-tag",
					"regex":  `^([0-9]+)$`,
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configDocument := map[string]any{
				"components": map[string]any{
					"test": map[string]any{
						"release": testCase.release,
					},
				},
			}

			err := schema.Validate(configDocument)
			if testCase.valid {
				require.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
