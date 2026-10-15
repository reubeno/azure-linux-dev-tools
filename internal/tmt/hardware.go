// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
)

type planExport struct {
	Name      string           `json:"name"`
	Provision []map[string]any `json:"provision"`
}

func resolvedPlanHardwareArgs(ctx opctx.Ctx, sourceDir, program, plan string, globalArgs ...string) ([]string, error) {
	hardware, _, err := resolvedPlanSettings(ctx, sourceDir, program, plan, globalArgs...)

	return hardware, err
}

func resolvedPlanSettings(ctx opctx.Ctx, sourceDir, program, plan string,
	globalArgs ...string,
) (hardwareArgs []string, user string, err error) {
	if ctx.DryRun() {
		return []string{"boot.method = uefi"}, "azureuser", nil
	}

	args := append(slices.Clone(globalArgs), "plan", "export", exactPlanName(plan), "--how", "json")

	output, err := commandOutput(ctx, sourceDir, program, args...)
	if err != nil {
		return nil, "", err
	}

	hardware, err := exportedPlanHardware([]byte(output), plan)
	if err != nil {
		return nil, "", err
	}

	selected, err := selectExportedPlan([]byte(output), plan)
	if err != nil {
		return nil, "", err
	}

	user = "azureuser"

	if len(selected.Provision) > 0 {
		if configured, exists := selected.Provision[0]["user"]; exists {
			value, ok := configured.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, "", errors.New("TMT virtual provision user must be a non-empty string")
			}

			user = value
		}
	}

	hardwareArgs, err = mergePlanHardwareArgs(hardware)

	return hardwareArgs, user, err
}

func exportedPlanHardware(output []byte, plan string) (map[string]any, error) {
	selected, err := selectExportedPlan(output, plan)
	if err != nil {
		return nil, err
	}

	if len(selected.Provision) > 0 {
		if value, exists := selected.Provision[0]["hardware"]; exists && value != nil {
			hardware, ok := value.(map[string]any)
			if !ok {
				return nil, errors.New("TMT virtual provision hardware must be a mapping")
			}

			return hardware, nil
		}
	}

	return map[string]any{}, nil
}

func selectExportedPlan(output []byte, plan string) (*planExport, error) {
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

	if len(selected.Provision) > 1 {
		return nil, fmt.Errorf("TMT VM plan %#q has multiple provision phases; per-guest overrides are not supported", plan)
	}

	return selected, nil
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
