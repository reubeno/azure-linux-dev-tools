// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package lisa runs LISA against disposable QEMU virtual machines.
package lisa

import (
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/pelletier/go-toml/v2"
)

type config struct {
	Source        *projectconfig.GitSourceConfig `toml:"source"`
	PipPreInstall []string                       `toml:"pip-pre-install"`
	PipExtras     []string                       `toml:"pip-extras"`
	ExtraArgs     []string                       `toml:"extra-args"`
}

type criteria struct {
	Name     string   `yaml:"name,omitempty"`
	Area     string   `yaml:"area,omitempty"`
	Category string   `yaml:"category,omitempty"`
	Priority any      `yaml:"priority,omitempty"`
	Tags     []string `yaml:"tags,omitempty"`
}

// Runner is an independently configured LISA adapter.
type Runner struct {
	name     string
	config   config
	criteria []criteria
}

var _ testframework.Runner = (*Runner)(nil)

// New decodes consumed keys while retaining compatibility with external metadata.
func New(name string, raw map[string]any) (*Runner, error) {
	if err := testframework.ValidateName(name); err != nil {
		return nil, fmt.Errorf("LISA test name:\n%w", err)
	}

	runner := &Runner{name: name}
	consumed := make(map[string]any)

	for _, key := range []string{"source", "pip-pre-install", "pip-extras", "extra-args"} {
		if value, ok := raw[key]; ok {
			consumed[key] = value
		}
	}

	data, err := toml.Marshal(consumed)
	if err != nil {
		return nil, fmt.Errorf("encode LISA configuration:\n%w", err)
	}

	if err := toml.Unmarshal(data, &runner.config); err != nil {
		return nil, fmt.Errorf("decode LISA configuration:\n%w", err)
	}

	if runner.config.Source != nil {
		if err := runner.config.Source.Validate("lisa.source"); err != nil {
			return nil, fmt.Errorf("LISA source:\n%w", err)
		}
	}

	runner.criteria, err = parseCriteria(raw)
	if err != nil {
		return nil, fmt.Errorf("LISA test %#q:\n%w", name, err)
	}

	return runner, nil
}

func parseCriteria(raw map[string]any) ([]criteria, error) {
	var entries []map[string]any

	if value, exists := raw["criteria"]; exists {
		switch typed := value.(type) {
		case map[string]any:
			entries = []map[string]any{typed}
		case []map[string]any:
			entries = typed
		case []any:
			for index, entry := range typed {
				table, ok := entry.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("LISA criteria entry %d must be a table", index)
				}

				entries = append(entries, table)
			}
		default:
			return nil, errors.New("LISA 'criteria' must be a table or list of tables")
		}
	}

	if len(entries) == 0 {
		top := make(map[string]any)
		for _, key := range []string{"name", "testcase-name", "testcase-names"} {
			if value, ok := raw[key]; ok {
				top[key] = value
			}
		}

		entries = []map[string]any{top}
	}

	result := make([]criteria, 0, len(entries))
	for _, entry := range entries {
		converted, err := convertCriteria(entry)
		if err != nil {
			return nil, err
		}

		result = append(result, converted)
	}

	return result, nil
}

func convertCriteria(raw map[string]any) (criteria, error) {
	result := criteria{}
	for key, target := range map[string]*string{
		"name": &result.Name, "area": &result.Area, "category": &result.Category,
	} {
		if value, exists := raw[key]; exists {
			text, ok := value.(string)
			if !ok {
				return result, fmt.Errorf("LISA criteria %#q must be a string", key)
			}

			*target = text
		}
	}

	name, err := selectedName(raw, result.Name)
	if err != nil {
		return result, err
	}

	result.Name = name

	if value, exists := raw["tags"]; exists {
		tags, err := stringList(value)
		if err != nil {
			return result, fmt.Errorf("LISA 'tags':\n%w", err)
		}

		result.Tags = tags
	}

	if value, exists := raw["priority"]; exists {
		priority, err := priorityValue(value)
		if err != nil {
			return result, err
		}

		result.Priority = priority
	}

	if result.Name == "" && result.Area == "" && result.Category == "" && result.Priority == nil && len(result.Tags) == 0 {
		return result, errors.New("LISA test requires nonempty selectors")
	}

	return result, nil
}

func selectedName(raw map[string]any, name string) (string, error) {
	if value, exists := raw["testcase-name"]; exists {
		text, ok := value.(string)
		if !ok {
			return "", errors.New("LISA 'testcase-name' must be a string")
		}

		if name == "" {
			name = text
		}
	}

	if value, exists := raw["testcase-names"]; exists {
		names, err := stringList(value)
		if err != nil {
			return "", fmt.Errorf("LISA 'testcase-names':\n%w", err)
		}

		if name == "" {
			name = strings.Join(names, "|")
		}
	}

	return name, nil
}

func stringList(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("expected a list of strings")
			}

			result = append(result, text)
		}

		return result, nil
	default:
		return nil, errors.New("expected a list of strings")
	}
}

func priorityValue(value any) (any, error) {
	switch typed := value.(type) {
	case int, int64:
		return typed, nil
	case []int:
		return append([]int(nil), typed...), nil
	case []int64:
		return append([]int64(nil), typed...), nil
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			switch item.(type) {
			case int, int64:
				result = append(result, item)
			default:
				return nil, errors.New("LISA 'priority' must contain integers")
			}
		}

		if len(result) == 0 {
			return nil, errors.New("LISA 'priority' list must not be empty")
		}

		return result, nil
	default:
		return nil, errors.New("LISA 'priority' must be an integer or list of integers")
	}
}
