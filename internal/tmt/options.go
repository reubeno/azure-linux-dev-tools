// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
)

func (c *config) decodeOptions(raw map[string]any) error {
	for _, field := range []struct {
		name   string
		target *[]string
	}{
		{"pip-extras", &c.PipExtras},
		{"run-extra-args", &c.RunExtraArgs},
		{"plan-extra-args", &c.PlanExtraArgs},
		{"cloud-init-runcmds", &c.CloudInitRuncmds},
	} {
		value, present := raw[field.name]
		if !present {
			continue
		}

		values, err := stringList(value)
		if err != nil {
			return fmt.Errorf("'tmt.%s':\n%w", field.name, err)
		}

		*field.target = values
	}

	if value, present := raw["context"]; present {
		if err := c.decodeContext(value); err != nil {
			return fmt.Errorf("'tmt.context':\n%w", err)
		}
	}

	if err := c.decodeProvision(raw); err != nil {
		return err
	}

	extraPattern := regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	for index, extra := range c.PipExtras {
		if !extraPattern.MatchString(extra) {
			return fmt.Errorf("'tmt.pip-extras' contains invalid extra %#q", extra)
		}

		c.PipExtras[index] = strings.Join(strings.FieldsFunc(strings.ToLower(extra), func(char rune) bool {
			return char == '-' || char == '_' || char == '.'
		}), "-")
	}

	for _, scope := range []struct {
		name string
		args []string
	}{
		{"run-extra-args", c.RunExtraArgs},
		{"plan-extra-args", c.PlanExtraArgs},
		{"provision.extra-args", c.Provision.ExtraArgs},
	} {
		if err := validateExtraArgs(scope.name, scope.args); err != nil {
			return fmt.Errorf("'tmt.%s':\n%w", scope.name, err)
		}
	}

	return nil
}

func stringList(value any) ([]string, error) {
	var values []string

	switch typed := value.(type) {
	case []string:
		values = slices.Clone(typed)
	case []any:
		values = make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("item %d must be a string, got %T", index, item)
			}

			values[index] = text
		}
	default:
		return nil, fmt.Errorf("must be an array of strings, got %T", value)
	}

	for index, text := range values {
		if strings.ContainsRune(text, 0) {
			return nil, fmt.Errorf("item %d contains a null byte", index)
		}
	}

	return values, nil
}

func (c *config) decodeContext(value any) error {
	c.Context = make(map[string][]string)

	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			values, err := stringList(item)
			if err != nil {
				return fmt.Errorf("dimension %#q:\n%w", key, err)
			}

			c.Context[key] = values
		}
	case map[string][]string:
		for key, item := range typed {
			c.Context[key] = slices.Clone(item)
		}
	default:
		return fmt.Errorf("must be a table of string arrays, got %T", value)
	}

	for key, values := range c.Context {
		if key == "" || strings.ContainsAny(key, "=@\x00") || strings.ContainsFunc(key, unicode.IsSpace) {
			return fmt.Errorf("invalid dimension name %#q", key)
		}

		if len(values) == 0 {
			return fmt.Errorf("dimension %#q must contain at least one value", key)
		}

		for _, item := range values {
			if item == "" || strings.ContainsRune(item, 0) {
				return fmt.Errorf("dimension %#q contains an empty value or null byte", key)
			}
		}
	}

	return nil
}

func (c *config) decodeProvision(raw map[string]any) error {
	provision := map[string]any{}

	if value, present := raw["provision"]; present {
		var ok bool

		provision, ok = value.(map[string]any)
		if !ok {
			return fmt.Errorf("'tmt.provision' must be a table, got %T", value)
		}
	}

	if value, present := provision["how"]; present {
		how, ok := value.(string)
		if !ok {
			return fmt.Errorf("'tmt.provision.how' must be a string, got %T", value)
		}

		if _, err := provisionStrategy(how); err != nil {
			return err
		}

		c.Provision.How = how
	}

	value, present := provision["extra-args"]
	if legacy, legacyPresent := raw["provision-extra-args"]; legacyPresent {
		if present {
			return errors.New("use only one of 'tmt.provision.extra-args' and 'tmt.provision-extra-args'")
		}

		value, present = legacy, true
	}

	if present {
		values, err := stringList(value)
		if err != nil {
			return fmt.Errorf("'tmt.provision.extra-args':\n%w", err)
		}

		c.Provision.ExtraArgs = values
	}

	return nil
}

func provisionStrategy(how string) (testframework.Strategy, error) {
	switch how {
	case "":
		return "", nil
	case "virtual":
		return testframework.VM, nil
	case "container":
		return testframework.Container, nil
	case "connect":
		return testframework.SSH, nil
	case "local":
		return testframework.Host, nil
	default:
		return "", fmt.Errorf("unsupported 'tmt.provision.how' %#q", how)
	}
}

func (c *config) validateStrategy(strategy testframework.Strategy) error {
	configured, err := provisionStrategy(c.Provision.How)
	if err != nil {
		return err
	}

	if configured != "" && configured != strategy {
		return fmt.Errorf("'tmt.provision.how' %#q requires strategy %#q, not %#q",
			c.Provision.How, configured, strategy)
	}

	if len(c.CloudInitRuncmds) != 0 && strategy != testframework.VM {
		return errors.New("'tmt.cloud-init-runcmds' requires strategy 'vm'")
	}

	return nil
}

// globalArgs preserves the PoC's pre-'run' scope for [config.RunExtraArgs].
func (c *config) globalArgs(verbose bool) []string {
	var args []string

	if verbose && !hasVerbosity(c.RunExtraArgs) {
		args = append(args, "-vv")
	}

	keys := make([]string, 0, len(c.Context))
	for key := range c.Context {
		keys = append(keys, key)
	}

	slices.Sort(keys)

	for _, key := range keys {
		for _, value := range c.Context[key] {
			args = append(args, "-c", key+"="+value)
		}
	}

	return append(args, c.RunExtraArgs...)
}

func hasVerbosity(args []string) bool {
	for _, arg := range args {
		if arg == "--verbose" || arg == "--quiet" ||
			(strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") &&
				strings.ContainsAny(arg, "vq")) {
			return true
		}
	}

	return false
}

func (c *config) installExtras(strategy testframework.Strategy) []string {
	extras := slices.Clone(c.PipExtras)
	if strategy == testframework.VM {
		extras = append(extras, "provision-virtual")
	}

	slices.Sort(extras)

	return slices.Compact(extras)
}

// Only known, step-scoped options are accepted: a denylist cannot safely determine
// which positional tokens are option values and which start another TMT step.
func validateExtraArgs(scope string, args []string) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]

		takesValue, attached, err := extraOption(scope, arg)
		if err != nil {
			return err
		}

		if !takesValue || attached {
			continue
		}

		if index+1 == len(args) || strings.HasPrefix(args[index+1], "-") || args[index+1] == "" {
			return fmt.Errorf("option %#q requires a value", arg)
		}

		index++
	}

	return nil
}

func extraOption(scope, arg string) (takesValue, attached bool, err error) {
	if strings.HasPrefix(arg, "--") {
		name, value, hasValue := strings.Cut(arg, "=")
		arity := optionArity(scope, name)

		if arity < 0 || (hasValue && (arity == 0 || value == "")) {
			return false, false, fmt.Errorf("unsupported or azldev-owned option %#q", arg)
		}

		return arity == 1, hasValue, nil
	}

	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return false, false, fmt.Errorf("unexpected positional argument %#q; additional TMT steps are not allowed", arg)
	}

	for index := 1; index < len(arg); index++ {
		arity := optionArity(scope, "-"+arg[index:index+1])
		if arity < 0 {
			return false, false, fmt.Errorf("unsupported or azldev-owned option in %#q", arg)
		}

		if arity == 1 {
			if arg[index+1:] == "=" {
				return false, false, fmt.Errorf("option %#q requires a value", arg)
			}

			return true, index+1 < len(arg), nil
		}
	}

	return false, false, nil
}

func optionArity(scope, name string) int {
	switch name {
	case "-v", "--verbose", "-d", "--debug", "-q", "--quiet":
		return 0
	}

	switch scope {
	case "run-extra-args":
		switch name {
		case "--no-color", "--force-color", "--show-time":
			return 0
		case "--log-topic":
			return 1
		}
	case "plan-extra-args":
		switch name {
		case "--shallow", "--enabled", "--disabled":
			return 0
		case "-f", "--filter", "-c", "--condition", "-l", "--link", "-x", "--exclude":
			return 1
		}
	case "provision.extra-args":
		switch name {
		case "-m", "--memory", "-D", "--disk", "-c", "--connection",
			"--stop-retries", "--stop-retry-delay", "--ssh-option", "--role":
			return 1
		}
	}

	return -1
}
