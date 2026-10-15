// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Package tmt runs pinned TMT plans against managed guests or caller-owned machines.
package tmt

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/microsoft/azure-linux-dev-tools/internal/projectconfig"
	"github.com/microsoft/azure-linux-dev-tools/internal/testframework"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/qemu"
)

type config struct {
	Source           *projectconfig.GitSourceConfig
	Plan             string
	Context          map[string][]string
	PipExtras        []string
	RunExtraArgs     []string
	PlanExtraArgs    []string
	Provision        provisionConfig
	CloudInitRuncmds []string
}

type provisionConfig struct {
	How       string
	ExtraArgs []string
}

// Runner adapts TMT's native provisioners to the shared test execution contract.
type Runner struct {
	name   string
	config config
}

var _ testframework.Runner = (*Runner)(nil)

// New validates and snapshots a test's TMT-specific configuration.
func New(name string, raw map[string]any) (*Runner, error) {
	if err := testframework.ValidateName(name); err != nil {
		return nil, fmt.Errorf("TMT test name:\n%w", err)
	}

	decoded, err := decodeConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("TMT test %#q:\n%w", name, err)
	}

	return &Runner{name: name, config: decoded}, nil
}

func decodeConfig(raw map[string]any) (config, error) {
	var decoded config

	if value, present := raw["source"]; present {
		source, ok := value.(map[string]any)
		if !ok {
			return decoded, fmt.Errorf("'tmt.source' must be a table, got %T", value)
		}

		gitURL, _ := source["git-url"].(string)
		ref, _ := source["ref"].(string)
		decoded.Source = &projectconfig.GitSourceConfig{GitURL: gitURL, Ref: ref}

		if err := decoded.Source.Validate("'tmt.source'"); err != nil {
			return decoded, fmt.Errorf("validate 'tmt.source':\n%w", err)
		}
	}

	if value, present := raw["plan"]; present {
		plan, ok := value.(string)
		if !ok {
			return decoded, fmt.Errorf("'tmt.plan' must be a string, got %T", value)
		}

		if err := validatePlan(plan); err != nil {
			return decoded, err
		}

		decoded.Plan = plan
	}

	if err := decoded.decodeOptions(raw); err != nil {
		return decoded, err
	}

	return decoded, nil
}

func validatePlan(plan string) error {
	if plan != "" && !strings.HasPrefix(plan, "/") {
		return errors.New("'tmt.plan' (or '--plan') must be an absolute plan name")
	}

	return nil
}

func (r *Runner) plan(execution testframework.ExecutionSpec) string {
	if execution.Plan != "" {
		return execution.Plan
	}

	return r.config.Plan
}

// Requirements validates execution choices before any artifacts or environments are acquired.
func (r *Runner) Requirements(execution testframework.ExecutionSpec) (testframework.Requirements, error) {
	if err := execution.Validate(); err != nil {
		return testframework.Requirements{}, fmt.Errorf("TMT execution:\n%w", err)
	}

	if err := r.validateExecution(execution); err != nil {
		return testframework.Requirements{}, err
	}

	requirements := testframework.Requirements{PackagesSupported: true}

	switch execution.Strategy {
	case testframework.VM:
		if r.plan(execution) == "" {
			return testframework.Requirements{}, errors.New(
				"'tmt.plan' (or '--plan') is required for TMT strategy 'vm'; hardware is resolved per-plan")
		}

		requirements.ImageRequired = true
		requirements.ImageFormats = qemu.DiskFormats()
		requirements.LiveImage = true
	case testframework.Container:
		requirements.ImageRequired = true
		requirements.ImageFormats = []string{"oci"}
		requirements.LiveImage = true
	case testframework.SSH, testframework.Host:
		// The machine is caller-owned; no image is consumed or destroyed.
	}

	return requirements, nil
}

func (r *Runner) validateExecution(execution testframework.ExecutionSpec) error {
	if execution.FrameworkDir != "" {
		return errors.New("'--framework-dir' is not supported by the pinned TMT adapter")
	}

	if execution.SourceDir == "" && r.config.Source == nil {
		return errors.New("'tmt.source' (git-url, ref) is required unless '--source-dir' is set")
	}

	if execution.SourceDir != "" && !filepath.IsAbs(execution.SourceDir) {
		return errors.New("'--source-dir' must be absolute")
	}

	if err := validatePlan(r.plan(execution)); err != nil {
		return err
	}

	if err := r.config.validateStrategy(execution.Strategy); err != nil {
		return err
	}

	if execution.Strategy == testframework.SSH {
		for _, option := range []struct{ name, value string }{
			{"host", execution.SSHHost}, {"user", execution.SSHUser},
		} {
			if strings.HasPrefix(option.value, "-") ||
				strings.ContainsAny(option.value, "@/\x00") ||
				strings.ContainsFunc(option.value, unicode.IsSpace) {
				return fmt.Errorf("invalid SSH %s %#q", option.name, option.value)
			}
		}

		if execution.SSHKey != "" && !filepath.IsAbs(execution.SSHKey) {
			return errors.New("SSH key path must be absolute")
		}
	}

	return nil
}
