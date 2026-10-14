// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

type planExport struct {
	Name      string           `json:"name"`
	Provision []map[string]any `json:"provision"`
}

func resolvedPlanHardwareArgs(ctx opctx.Ctx, sourceDir, program, plan string) ([]string, error) {
	if ctx.DryRun() {
		return []string{"boot.method = uefi"}, nil
	}

	output, err := commandOutput(ctx, sourceDir, program, "plan", "export", plan, "--how", "json")
	if err != nil {
		return nil, err
	}

	hardware, err := exportedPlanHardware([]byte(output), plan)
	if err != nil {
		return nil, err
	}

	return mergePlanHardwareArgs(hardware)
}

func exportedPlanHardware(output []byte, plan string) (map[string]any, error) {
	var plans []planExport
	if err := json.Unmarshal(output, &plans); err != nil {
		return nil, fmt.Errorf("decode TMT plan export:\n%w", err)
	}

	var selected *planExport

	for index := range plans {
		if plans[index].Name != plan {
			continue
		}

		if selected != nil {
			return nil, fmt.Errorf("multiple exported plans are named %#q", plan)
		}

		selected = &plans[index]
	}

	if selected == nil {
		return nil, fmt.Errorf("plan %#q was not returned by TMT plan export", plan)
	}

	if len(selected.Provision) > 0 {
		if hardware, ok := selected.Provision[0]["hardware"].(map[string]any); ok {
			return hardware, nil
		}
	}

	return map[string]any{}, nil
}

// CLI hardware replaces rather than extends the plan's hardware. Re-emit all
// constraints to retain memory, disk and CPU requirements while forcing UEFI.
func mergePlanHardwareArgs(hardware map[string]any) ([]string, error) {
	constraints, err := flattenHardwareConstraints("", hardware)
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, len(constraints)+1)
	for _, constraint := range constraints {
		if strings.HasPrefix(constraint, "boot.method ") {
			continue
		}

		args = append(args, constraint)
	}

	return append(args, "boot.method = uefi"), nil
}

// Lists use TMT's indexed syntax. Boolean groups cannot be flattened without
// changing their meaning, so fail instead of silently weakening the plan.
//
//nolint:cyclop // Each supported JSON type has distinct serialization.
func flattenHardwareConstraints(prefix string, value any) ([]string, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}

		sort.Strings(keys)

		var constraints []string

		for _, key := range keys {
			if key == "and" || key == "or" {
				return nil, fmt.Errorf("hardware constraint group %#q cannot be safely overridden", key)
			}

			nextPrefix := key
			if prefix != "" {
				nextPrefix = prefix + "." + key
			}

			child, err := flattenHardwareConstraints(nextPrefix, typed[key])
			if err != nil {
				return nil, err
			}

			constraints = append(constraints, child...)
		}

		return constraints, nil
	case []any:
		var constraints []string

		for index, item := range typed {
			child, err := flattenHardwareConstraints(fmt.Sprintf("%s[%d]", prefix, index), item)
			if err != nil {
				return nil, err
			}

			constraints = append(constraints, child...)
		}

		return constraints, nil
	case string:
		if prefix == "" {
			return nil, errors.New("hardware constraint has no key")
		}

		return []string{prefix + " " + typed}, nil
	case float64, bool:
		if prefix == "" {
			return nil, errors.New("hardware constraint has no key")
		}

		return []string{fmt.Sprintf("%s = %v", prefix, typed)}, nil
	default:
		return nil, fmt.Errorf("unsupported hardware constraint %#q of type %T", prefix, value)
	}
}
