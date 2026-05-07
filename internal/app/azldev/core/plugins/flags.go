// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package plugins

import (
	"errors"
	"fmt"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
)

// ErrUnsupportedSchema indicates that a tool's input schema uses a JSON
// Schema feature that the MVP plugin loader cannot represent as a Cobra
// flag. Callers wrap this error with the offending tool name and skip
// registration, so other tools from the same plugin remain usable.
var ErrUnsupportedSchema = errors.New("unsupported input schema")

// Supported JSON-Schema scalar types in MVP. Anything else triggers
// [ErrUnsupportedSchema] for the entire tool.
const (
	jsonTypeString  = "string"
	jsonTypeInteger = "integer"
	jsonTypeNumber  = "number"
	jsonTypeBoolean = "boolean"
)

// schemaProperty is a thin typed view over the untyped JSON Schema property
// values returned by mcp-go. It only surfaces the fields the MVP cares about.
type schemaProperty struct {
	// jsonType is the value of the "type" keyword. Required; multi-type
	// (array of types) is rejected as unsupported.
	jsonType string
	// description is the value of the "description" keyword (may be empty).
	description string
	// hasDefault is true if the property had a "default" keyword.
	hasDefault bool
	// defaultValue holds the raw value of the "default" keyword.
	defaultValue any
}

// addToolFlags registers Cobra flags for the supported subset of tool's input
// schema, and marks required flags with [cobra.Command.MarkFlagRequired].
//
// The function validates the entire schema before mutating cmd, so on error
// the command is left untouched and the caller may skip registration of this
// particular tool (other tools from the same plugin remain usable).
func addToolFlags(cmd *cobra.Command, tool mcp.Tool) error {
	props, err := parseToolProperties(tool)
	if err != nil {
		return err
	}

	requiredSet := make(map[string]bool, len(tool.InputSchema.Required))
	for _, name := range tool.InputSchema.Required {
		requiredSet[name] = true
	}

	// Iterate in name order for deterministic registration (and stable help
	// output across runs).
	for _, name := range sortedKeys(props) {
		prop := props[name]

		if err := registerFlag(cmd, name, prop); err != nil {
			// Validation has already passed, so a failure here is unexpected.
			return fmt.Errorf("failed to register flag %#q on tool %#q:\n%w",
				name, tool.Name, err)
		}

		if requiredSet[name] {
			if err := cmd.MarkFlagRequired(name); err != nil {
				return fmt.Errorf("failed to mark flag %#q required on tool %#q:\n%w",
					name, tool.Name, err)
			}
		}
	}

	return nil
}

// collectFlagValues reads the user-supplied flag values back into a JSON-
// compatible map suitable for use as an MCP tool's "arguments" parameter.
//
// Only flags the user explicitly set are included, so the plugin's own
// defaults (declared in the tool's JSON Schema) apply when a flag was left
// at its Cobra default. This avoids the trap where the Cobra default
// (typically the type's zero value) would silently override a meaningful
// schema default.
func collectFlagValues(cmd *cobra.Command, tool mcp.Tool) (args map[string]any, err error) {
	props, err := parseToolProperties(tool)
	if err != nil {
		return nil, err
	}

	args = make(map[string]any, len(props))

	for name, prop := range props {
		if !cmd.Flags().Changed(name) {
			continue
		}

		value, err := readFlagValue(cmd, name, prop)
		if err != nil {
			return nil, fmt.Errorf("failed to read flag %#q on tool %#q:\n%w",
				name, tool.Name, err)
		}

		args[name] = value
	}

	return args, nil
}

// parseToolProperties validates and converts the tool's untyped JSON Schema
// properties to a typed map. Returns [ErrUnsupportedSchema] (wrapped) if any
// property uses a feature the MVP can't represent.
func parseToolProperties(tool mcp.Tool) (map[string]schemaProperty, error) {
	if tool.InputSchema.Type != "" && tool.InputSchema.Type != "object" {
		return nil, fmt.Errorf("%w: tool %#q top-level schema type %#q is not 'object'",
			ErrUnsupportedSchema, tool.Name, tool.InputSchema.Type)
	}

	props := make(map[string]schemaProperty, len(tool.InputSchema.Properties))

	for name, raw := range tool.InputSchema.Properties {
		prop, err := parseProperty(name, raw)
		if err != nil {
			return nil, fmt.Errorf("tool %#q property %#q: %w", tool.Name, name, err)
		}

		props[name] = prop
	}

	return props, nil
}

// parseProperty converts a single raw schema property to a typed
// [schemaProperty] and returns [ErrUnsupportedSchema] (wrapped) if the
// property uses unsupported keywords or types.
func parseProperty(name string, raw any) (schemaProperty, error) {
	obj, isObject := raw.(map[string]any)
	if !isObject {
		return schemaProperty{}, fmt.Errorf("%w: not a JSON object", ErrUnsupportedSchema)
	}

	// "type" must be a single string value naming a supported scalar.
	typeRaw, hasType := obj["type"]
	if !hasType {
		return schemaProperty{}, fmt.Errorf("%w: missing 'type'", ErrUnsupportedSchema)
	}

	typeStr, ok := typeRaw.(string)
	if !ok {
		return schemaProperty{}, fmt.Errorf("%w: 'type' is not a single string (multi-type unsupported)",
			ErrUnsupportedSchema)
	}

	switch typeStr {
	case jsonTypeString, jsonTypeInteger, jsonTypeNumber, jsonTypeBoolean:
		// supported
	default:
		return schemaProperty{}, fmt.Errorf("%w: type %#q is not supported in MVP",
			ErrUnsupportedSchema, typeStr)
	}

	// Reject keywords that imply structure beyond the MVP scalar subset.
	for _, keyword := range []string{"properties", "items", "oneOf", "anyOf", "allOf"} {
		if _, present := obj[keyword]; present {
			return schemaProperty{}, fmt.Errorf("%w: keyword %#q is not supported in MVP",
				ErrUnsupportedSchema, keyword)
		}
	}

	prop := schemaProperty{jsonType: typeStr}
	if d, ok := obj["description"].(string); ok {
		prop.description = d
	}

	if def, ok := obj["default"]; ok {
		prop.hasDefault = true
		prop.defaultValue = def
	}

	_ = name // reserved for future error reporting

	return prop, nil
}

// registerFlag adds a single Cobra flag for the given property to cmd.
// Validation must have happened in parseToolProperties first; this function
// only fails if a typed default value can't be coerced into the flag type.
func registerFlag(cmd *cobra.Command, name string, prop schemaProperty) error {
	switch prop.jsonType {
	case jsonTypeString:
		def, err := defaultAsString(prop)
		if err != nil {
			return err
		}

		cmd.Flags().String(name, def, prop.description)
	case jsonTypeInteger:
		def, err := defaultAsInt64(prop)
		if err != nil {
			return err
		}

		cmd.Flags().Int64(name, def, prop.description)
	case jsonTypeNumber:
		def, err := defaultAsFloat64(prop)
		if err != nil {
			return err
		}

		cmd.Flags().Float64(name, def, prop.description)
	case jsonTypeBoolean:
		def, err := defaultAsBool(prop)
		if err != nil {
			return err
		}

		cmd.Flags().Bool(name, def, prop.description)
	default:
		// Should never happen — validated in parseProperty.
		return fmt.Errorf("internal: unsupported flag type %#q", prop.jsonType)
	}

	return nil
}

// readFlagValue fetches the value of a previously-registered flag and
// returns it typed for use as an MCP tool argument.
func readFlagValue(cmd *cobra.Command, name string, prop schemaProperty) (any, error) {
	switch prop.jsonType {
	case jsonTypeString:
		value, err := cmd.Flags().GetString(name)
		if err != nil {
			return nil, fmt.Errorf("get string flag %#q:\n%w", name, err)
		}

		return value, nil
	case jsonTypeInteger:
		value, err := cmd.Flags().GetInt64(name)
		if err != nil {
			return nil, fmt.Errorf("get int flag %#q:\n%w", name, err)
		}

		return value, nil
	case jsonTypeNumber:
		value, err := cmd.Flags().GetFloat64(name)
		if err != nil {
			return nil, fmt.Errorf("get number flag %#q:\n%w", name, err)
		}

		return value, nil
	case jsonTypeBoolean:
		value, err := cmd.Flags().GetBool(name)
		if err != nil {
			return nil, fmt.Errorf("get bool flag %#q:\n%w", name, err)
		}

		return value, nil
	default:
		return nil, fmt.Errorf("internal: unsupported flag type %#q", prop.jsonType)
	}
}

// defaultAsString returns the property's default value coerced to a Go
// string, or the type's zero value when no default was specified.
func defaultAsString(prop schemaProperty) (string, error) {
	if !prop.hasDefault {
		return "", nil
	}

	value, isString := prop.defaultValue.(string)
	if !isString {
		return "", fmt.Errorf("default value %v is not a string", prop.defaultValue)
	}

	return value, nil
}

// defaultAsInt64 returns the property's default value coerced to int64. JSON
// numbers arrive as float64 from the JSON-RPC decoder; we narrow them after
// confirming there's no fractional component.
func defaultAsInt64(prop schemaProperty) (int64, error) {
	if !prop.hasDefault {
		return 0, nil
	}

	switch value := prop.defaultValue.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("default value %v is not an integer", value)
		}

		return int64(value), nil
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	default:
		return 0, fmt.Errorf("default value %v is not numeric", value)
	}
}

// defaultAsFloat64 returns the property's default value coerced to float64.
func defaultAsFloat64(prop schemaProperty) (float64, error) {
	if !prop.hasDefault {
		return 0, nil
	}

	switch value := prop.defaultValue.(type) {
	case float64:
		return value, nil
	case int64:
		return float64(value), nil
	case int:
		return float64(value), nil
	default:
		return 0, fmt.Errorf("default value %v is not numeric", value)
	}
}

// defaultAsBool returns the property's default value coerced to bool.
func defaultAsBool(prop schemaProperty) (bool, error) {
	if !prop.hasDefault {
		return false, nil
	}

	v, ok := prop.defaultValue.(bool)
	if !ok {
		return false, fmt.Errorf("default value %v is not a boolean", prop.defaultValue)
	}

	return v, nil
}

// sortedKeys returns the keys of m in sorted order. Used for deterministic
// flag-registration sequencing (and stable test/help output).
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}
